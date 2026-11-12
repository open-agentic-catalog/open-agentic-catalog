#!/usr/bin/env python3
"""Opt-in, read-only staged checks; installation only owns one Git hook file."""

import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import tomllib


GITLEAKS_VERSION = "8.30.1"
TIMEOUT = 30
OUTPUT_LIMIT = 8 << 20
POLICIES = ("test/hooks/gitleaks.toml", "test/hooks/gitleaksignore")
DATA_VALIDATOR = "test/packaging/data_checks.py"
YAML_POLICY = ".yamllint.yml"
STYLE_EXCLUDES = ("internal/a2apb/", "schemas/upstream/", "samples/upstream/")
GO_ROOTS = {"api", "cmd", "examples", "internal", "migrations", "schemas", "samples", "test", "catalog-ui"}
WRAPPER = Path(__file__).with_name("pre-commit")
LINTER_PINS = "test/ci/pins.json"
PYTHON_POLICIES = ("ruff.toml", "pyproject.toml", "uv.lock")
MARKDOWN_POLICY = ".markdownlint.json"
# These selections follow shell-check, python-lint, docs-check and container-check.
SHELL_FILES = {"test/functional/housekeeping.sh", "test/functional/restore.sh", "test/functional/pitr_archive.sh",
               "deployments/container/licenses.sh", "test/hooks/pre-commit"}
CONTAINER_FILES = {"Containerfile", "test/functional/Containerfile", "catalog-ui/Containerfile", "test/interoperability/adk/Containerfile",
                   "test/interoperability/Containerfile", "test/embeddings/Containerfile",
                   "test/ci/Containerfile.act", "test/mcp-scanner/Containerfile",
                   "examples/tutorials/local-oidc/Containerfile",
                   *(f"test/interoperability/{name}/Containerfile" for name in
                     ("mcp", "mcp/catalog", "a2a", "skills", "plugins", "agntcy/router", "ans")),
                   "test/interoperability/gateway/Containerfile.auth"}
MARKDOWN_EXCLUDES = ("schemas/upstream/", "samples/upstream/", "specs/", "output/", ".tools/",
                     "internal/adapter/testdata/upstream/openai/", "internal/adapter/testdata/upstream/huggingface/",
                     "internal/adapter/testdata/upstream/context7/plugin/",
                     "documentation/.astro/", "documentation/dist/", "documentation/src/content/docs/generated/",
                     "catalog-ui/dist/", "test/clientcompat/go/generated/", "sdk/rust/target/",
                     "test/sdk/rust/consumer/target/", "examples/rust-catalog-report/target/",
                     "extensions/vscode/dist/")
PYTHON_ROOTS = ("test/", "extensions/vscode/tooling/", "examples/langgraph/", "examples/tutorials/local-oidc/",
                "examples/mcp-scanner/")
TOOL_PATHS = {"ruff": ".tools/dev/bin/ruff", "shellcheck": ".tools/ci/bin/shellcheck",
              "hadolint": ".tools/ci/bin/hadolint", "actionlint": ".tools/ci/bin/actionlint",
              "markdownlint": ".tools/ci/markdown/node_modules/.bin/markdownlint"}


def is_go(name):
    """Select owned Go formatting without generated, linked or dependency trees."""
    return (name.endswith(".go") and name.split("/", 1)[0] in GO_ROOTS
            and not name.startswith(STYLE_EXCLUDES) and "node_modules" not in Path(name).parts)


def lint_kind(name):
    """Select only maintained file scopes; filenames are literal Git paths."""
    parts = Path(name).parts
    if any(part in {"node_modules", ".tools", ".venv", "__pycache__"} for part in parts):
        return None
    if name.startswith(".github/workflows/") and len(parts) == 3 and name.endswith((".yml", ".yaml")):
        return "actionlint"
    if name in SHELL_FILES:
        return "shellcheck"
    if name in CONTAINER_FILES:
        return "hadolint"
    if name.startswith(PYTHON_ROOTS) and name.endswith(".py") and not name.startswith("test/clientcompat/go/generated/"):
        return "ruff"
    if name.endswith((".md", ".mdx")) and not name.startswith(MARKDOWN_EXCLUDES):
        return "markdownlint"
    return None


def lint_locations(tool, raw, names, directory, stdin_name=None):
    """Project only known source paths, rule identifiers and positive line numbers."""
    findings = json.loads(raw)
    if tool == "shellcheck":
        findings = findings["comments"]
    if not isinstance(findings, list) or not findings:
        raise ValueError("missing findings")
    locations = []
    for item in findings[:10]:
        path = stdin_name or item.get("filename", item.get("filepath", item.get("fileName", item.get("file"))))
        if not isinstance(path, str):
            raise ValueError("missing path")
        if Path(path).is_absolute():
            path = str(Path(path).relative_to(directory))
        if path not in names:
            raise ValueError("unknown path")
        line = item.get("lineNumber", item.get("line"))
        code = item.get("code", item.get("kind"))
        if tool == "ruff":
            line = item["location"]["row"]
        elif tool == "markdownlint":
            code = item["ruleNames"][0]
        elif tool == "shellcheck":
            code = "SC" + str(code)
        if type(line) is not int or line < 1 or not isinstance(code, str) or not re.fullmatch(r"[A-Za-z][A-Za-z0-9-]{0,63}", code):
            raise ValueError("invalid location")
        locations.append(json.dumps({"file": path, "rule": code, "line": line}, ensure_ascii=True))
    return locations


class Failure(Exception):
    """A bounded user-facing message without tool output or source content."""


class Checks:
    """Inspect one repository index under a shared wall-clock budget."""

    def __init__(self, directory=None):
        """Resolve the repository root once and start the whole-check deadline."""
        self.directory = directory
        self.deadline = time.monotonic() + TIMEOUT
        # Git appends one newline; additional newlines may belong to the path.
        self.root = Path(os.fsdecode(self.git("rev-parse", "--show-toplevel").stdout[:-1]))
        self.directory = self.root

    def run(self, arguments, data=None, *, directory=None, environment=None, capture_errors=False):
        """Run within the remaining budget, retaining only bounded private stdout.

        A timeout or interrupt kills the owned process group; stderr is discarded
        so scanner or Git failures cannot copy source content into diagnostics.
        """
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise Failure("checks exceeded 30 seconds; reduce the staged change or retry")
        # Anonymous temporary output prevents verbose tool failures from filling
        # memory or copying source/credentials into terminal diagnostics.
        with tempfile.TemporaryFile() as output:
            try:
                process = subprocess.Popen(arguments, cwd=directory or self.directory, env=environment, stdin=subprocess.PIPE,
                                           stdout=output, stderr=output if capture_errors else subprocess.DEVNULL,
                                           start_new_session=True)
            except OSError:
                raise Failure("required tool unavailable; install the selected pinned tool before checking") from None
            try:
                process.communicate(data, timeout=remaining)
            except (subprocess.TimeoutExpired, KeyboardInterrupt):
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                raise Failure("check timed out or was interrupted; retry explicitly") from None
            if output.tell() > OUTPUT_LIMIT:
                raise Failure("tool output exceeds 8 MiB; reduce the staged change")
            output.seek(0)
            return subprocess.CompletedProcess(arguments, process.returncode, output.read())

    def git(self, *arguments, allowed=(0,)):
        """Require an expected Git exit status without exposing its raw output on error."""
        result = self.run(["git", *arguments])
        if result.returncode not in allowed:
            raise Failure("Git could not inspect the index; resolve conflicts and retry")
        return result

    def prerequisites(self):
        """Require the pinned scanner for every nonempty change, without installation."""
        version = self.run(["gitleaks", "version"])
        if version.returncode or version.stdout.strip() != GITLEAKS_VERSION.encode():
            raise Failure("install the documented Gitleaks " + GITLEAKS_VERSION + " binary before enabling checks")

    def entry(self, name):
        """Look up exactly one literal index path, including unusual filenames."""
        return self.git("ls-files", "--stage", "-z", "--", ":(literal)" + name).stdout

    def blob(self, name):
        """Read a regular staged file, rejecting links and other index modes."""
        entry = self.entry(name)
        if not entry.startswith((b"100644 ", b"100755 ")):
            raise Failure("staged file must be regular: " + json.dumps(name, ensure_ascii=True))
        return self.git("cat-file", "blob", entry.split(b" ", 2)[1].decode("ascii")).stdout

    def policy(self, name, label):
        """Return bounded staged policy bytes only when the working copy agrees."""
        path = self.root / name
        try:
            if (any((self.root / parent).is_symlink() for parent in Path(name).parents)
                    or not stat.S_ISREG(path.lstat().st_mode) or path.stat().st_size > OUTPUT_LIMIT):
                raise Failure(label + " must be a regular repository file")
            working = path.read_bytes()
        except OSError:
            raise Failure(label + " missing; restore the repository hook files") from None
        staged = self.blob(name)
        if staged != working:
            raise Failure(label + " has unstaged edits; stage or resolve them before checking")
        return staged

    def policies(self):
        """Return verified scanner policy bytes for installation or private snapshots."""
        return {name: self.policy(name, "scanner policy") for name in POLICIES}

    def data(self, paths):
        """Check staged JSON/YAML with verified validator and policy snapshots."""
        names = [os.fsdecode(raw) for raw in paths.split(b"\0") if raw]
        names = [name for name in names if Path(name).suffix in {".json", ".jsonld", ".yaml", ".yml"}]
        if not names:
            return
        validator = self.policy(DATA_VALIDATOR, "data validator")
        with tempfile.TemporaryDirectory(prefix="catalog-staged-data-") as directory:
            script = Path(directory) / "data_checks.py"
            script.write_bytes(validator)
            # Load only the verified snapshot, never an unchecked working-tree
            # import; reuse both the strict parser and existing YAML scope rules.
            try:
                spec = importlib.util.spec_from_file_location("catalog_staged_data", script)
                module = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(module)
                if not callable(module.validate_json) or not callable(module.yaml_scope):
                    raise ValueError("missing validator entry points")
            except Exception:
                raise Failure("staged data validator could not be loaded") from None
            policy = None
            for name in names:
                if time.monotonic() >= self.deadline:
                    raise Failure("checks exceeded 30 seconds; reduce the staged change or retry")
                source = self.blob(name)
                if Path(name).suffix in {".json", ".jsonld"}:
                    try:
                        module.validate_json(source)
                    except (ValueError, RecursionError):
                        raise Failure("invalid staged JSON: " + json.dumps(name, ensure_ascii=True)) from None
                    except Exception:
                        raise Failure("staged data validator failed") from None
                else:
                    # The hook also owns staged Control OpenAPI syntax; the
                    # whole-worktree helper delegates it to api-yaml-check.
                    try:
                        scope = "maintained" if name == "api/openapi/control-v1.yaml" else module.yaml_scope(name)
                        if scope not in (None, "maintained", "upstream"):
                            raise ValueError("unknown YAML scope")
                    except Exception:
                        raise Failure("staged data validator failed") from None
                    if scope is None:
                        continue  # Raw Helm templates need rendered validation.
                    if scope == "upstream":
                        options = ["-d", '{"rules": {"key-duplicates": "enable"}}']
                    else:
                        if policy is None:
                            policy = Path(directory) / "yamllint.yml"
                            policy.write_bytes(self.policy(YAML_POLICY, "YAML policy"))
                        options = ["-c", str(policy)]
                    result = self.run(["yamllint", *options, "-"], source)
                    if result.returncode:
                        raise Failure("staged YAML validation failed: " + json.dumps(name, ensure_ascii=True))

    def executable(self, name):
        """Prefer an explicit command, then owned tools, then PATH; never acquire tools."""
        explicit = os.environ.get(name.upper())
        selected = explicit or (str(self.root / TOOL_PATHS[name]) if (self.root / TOOL_PATHS[name]).is_file() else name)
        executable = shutil.which(selected)
        if executable is None:
            raise Failure("required tool unavailable; install the pinned " + name + " before checking")
        return str(Path(executable).absolute())

    def linters(self, paths):
        """Lint staged files in a private tree with explicit policy and clean tool inputs."""
        groups = {}
        for name in paths:
            if kind := lint_kind(name):
                groups.setdefault(kind, []).append(name)
        if not groups:
            return
        try:
            pins = json.loads(self.policy(LINTER_PINS, "linter pins")) if set(groups) - {"ruff"} else {}
            python_policies = {}
            if "ruff" in groups:
                python_policies = {name: self.policy(name, "Python policy") for name in PYTHON_POLICIES}
                ruff, development, lock = (tomllib.loads(python_policies[name].decode("utf-8")) for name in PYTHON_POLICIES)
                version = ruff["required-version"].removeprefix("==")
                if ("extend" in ruff or "ruff==" + version not in development["dependency-groups"]["dev"]
                        or not any(p.get("name") == "ruff" and p.get("version") == version for p in lock["package"])):
                    raise ValueError("inconsistent Ruff policy")
                pins["ruff"] = version
            if "actionlint" in groups:
                pins["actionlint"] = pins["actionlint"]["version"]
            if "markdownlint" in groups:
                pins["markdownlint"] = pins["markdownlint-cli"]
            needed = set(groups) | ({"shellcheck"} if "actionlint" in groups else set())
            if any(not isinstance(pins.get(tool), str) or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", pins[tool]) for tool in needed):
                raise ValueError("invalid tool pin")
        except (ValueError, KeyError, TypeError, AttributeError, RecursionError, UnicodeError):
            raise Failure("staged linter pins or Python policies are invalid or inconsistent") from None

        with tempfile.TemporaryDirectory(prefix="catalog-staged-lint-") as temporary:
            directory = Path(temporary)
            environment = {key: os.environ[key] for key in ("PATH", "LANG", "LC_ALL") if key in os.environ}
            environment["PYTHONDONTWRITEBYTECODE"] = "1"
            commands = {}
            for tool in sorted(needed):
                commands[tool] = self.executable(tool)
                flag = "-version" if tool == "actionlint" else "--version"
                version = self.run([commands[tool], flag], directory=directory, environment=environment, capture_errors=True)
                pattern = r"(?<![0-9.])v?" + re.escape(pins[tool]) + r"(?![0-9.])"
                if version.returncode or not re.search(pattern, version.stdout.decode("utf-8", errors="replace")):
                    raise Failure("install the pinned " + tool + " " + pins[tool] + " before checking")
            policy = directory / ".hook-policy"
            policy.mkdir()
            empty = policy / "empty.yaml"
            empty.write_text("{}\n")
            if "ruff" in groups:
                # Use the verified bytes, not a subsequent working-tree read.
                (directory / "ruff.toml").write_bytes(python_policies["ruff.toml"])
            if "markdownlint" in groups:
                raw = self.policy(MARKDOWN_POLICY, "Markdown policy")
                try:
                    config = json.loads(raw)
                    if not isinstance(config, dict) or "extends" in config:
                        raise ValueError("external Markdown policy")
                except (ValueError, UnicodeError):
                    raise Failure("staged Markdown policy must be a self-contained JSON object") from None
                (policy / "markdown.json").write_bytes(raw)
            for tool, names in groups.items():
                for name in names:
                    destination = directory / name
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    destination.write_bytes(self.blob(name))
                if tool == "ruff":
                    arguments = ["check", "--config", str(directory / "ruff.toml"), "--no-cache", "--no-fix",
                                 "--force-exclude", "--output-format", "json", "--", *names]
                elif tool == "shellcheck":
                    arguments = ["--norc", "--format=json1", "--", *names]
                elif tool == "hadolint":
                    arguments = ["--config", str(empty), "--no-color", "--format", "json", *names]
                elif tool == "actionlint":
                    # actionlint adds -x to ShellCheck. Do not let staged scripts
                    # cause it to read unstaged or absolute source paths.
                    wrapper = policy / "shellcheck"
                    wrapper.write_text("#!/usr/bin/env python3\nimport os, sys\n"
                                       + "program = " + repr(commands["shellcheck"]) + "\n"
                                       + "os.execv(program, [program, *[a for a in sys.argv[1:] if a != '-x']])\n")
                    wrapper.chmod(0o700)
                    arguments = ["-config-file", str(empty), "-no-color", "-pyflakes=",
                                 "-shellcheck=" + str(wrapper), "-format", "{{json .}}", *names]
                else:
                    # The Markdown CLI expands globs in filenames. STDIN keeps
                    # names such as "[draft].md" literal without loading a plugin.
                    for name in names:
                        self.lint(tool, commands[tool], ["--config", str(policy / "markdown.json"),
                                  "--ignore-path", "/dev/null", "--json", "--stdin"],
                                  names, directory, environment, self.blob(name), stdin_name=name)
                    continue
                self.lint(tool, commands[tool], arguments, names, directory, environment)

    def lint(self, tool, executable, arguments, names, directory, environment, source=None, *, stdin_name=None):
        """Report bounded locations, never arbitrary linter messages or source snippets."""
        result = self.run([executable, *arguments], source, directory=directory, environment=environment, capture_errors=True)
        if result.returncode:
            try:
                locations = lint_locations(tool, result.stdout, names, directory, stdin_name)
            except (ValueError, KeyError, TypeError, IndexError, AttributeError, RecursionError):
                raise Failure(tool + " failed; check the pinned tool and staged inputs") from None
            raise Failure(tool + " rejected staged files (first 10 locations at most):\n" + "\n".join(locations))

    def check(self):
        """Check staged secrets, data syntax, Go style and whitespace without edits.

        Go source is read from index blobs, not working files, so partially staged
        changes are checked as they will be committed. Findings expose locations only.
        """
        # Reject unmerged index entries even when their diff has no added text.
        if self.git("ls-files", "--unmerged", "-z").stdout:
            raise Failure("resolve staged merge conflicts before committing")
        names = self.git("diff", "--cached", "--name-only", "--no-renames", "-z").stdout
        if not names:
            print("No staged changes to check.")
            return
        self.prerequisites()
        policies = self.policies()
        with tempfile.TemporaryDirectory(prefix="catalog-staged-scanner-") as directory:
            paths = []
            for name in POLICIES:
                path = Path(directory) / Path(name).name
                path.write_bytes(policies[name])
                paths.append(str(path))
            result = self.run(["gitleaks", "git", "--staged", "--redact=100", "--no-banner", "--no-color",
                               "--log-level", "error", "--ignore-gitleaks-allow", "--config", paths[0],
                               "--gitleaks-ignore-path", paths[1], "--report-format", "json",
                               "--report-path", "/dev/stdout", "."])
        if result.returncode:
            # Only the selected safe fields are displayed, not the matched line
            # or an arbitrary scanner message (even with scanner redaction on).
            try:
                findings = json.loads(result.stdout)
            except (ValueError, UnicodeError):
                findings = None
            if result.returncode != 1 or not isinstance(findings, list) or not findings:
                raise Failure("secret scanner failed; check installation/configuration and retry")
            locations = []
            for finding in findings[:10]:
                if not isinstance(finding, dict):
                    raise Failure("secret scanner returned invalid findings")
                location = {key: finding.get(key) for key in ("File", "RuleID", "StartLine")}
                locations.append(json.dumps(location, ensure_ascii=True)[:1024])
            raise Failure("possible staged secrets (first 10 locations at most):\n" + "\n".join(locations))
        paths = self.git("diff", "--cached", "--name-only", "--no-renames", "--diff-filter=ACMRT", "-z").stdout
        self.data(paths)
        for raw in paths.split(b"\0"):
            name = os.fsdecode(raw)
            if not is_go(name):
                continue
            entry = self.entry(name)
            if not entry.startswith((b"100644 ", b"100755 ")):
                continue  # A symlink's target is metadata, never a file to open.
            blob = entry.split(b" ", 2)[1].decode("ascii")
            source = self.git("cat-file", "blob", blob).stdout
            # A filename makes gofmt require a complete Go file. Bare stdin
            # accepts fragments (for example a declaration without a package).
            formatted = self.run(["gofmt", "/dev/stdin"], source)
            if formatted.returncode or formatted.stdout != source:
                raise Failure("format or fix staged Go file " + json.dumps(name, ensure_ascii=True) + "; then stage the intended changes")
        self.linters([os.fsdecode(raw) for raw in paths.split(b"\0") if raw])
        whitespace = self.git("diff", "--cached", "--check", "--no-ext-diff", "--no-textconv", "--", ".",
                              ":(exclude)internal/a2apb/**", ":(exclude)schemas/upstream/**", allowed=(0, 2))
        if whitespace.returncode:
            raise Failure("staged whitespace errors or conflict markers; inspect git diff --cached --check locally (it can display source)")
        print("Staged secrets, JSON/YAML, Go formatting, file linters and whitespace checks passed.")

    def hook(self, install):
        """Install or remove only this exact hook, preserving custom hooks and paths."""
        custom = self.git("config", "--get", "core.hooksPath", allowed=(0, 1))
        if custom.returncode == 0:
            raise Failure("custom core.hooksPath is configured; use python3 -B test/hooks/runner.py check from your existing hook")
        path = Path(os.fsdecode(self.git("rev-parse", "--git-path", "hooks/pre-commit").stdout[:-1]))
        if not path.is_absolute():
            path = self.root / path
        if path.parent.is_symlink():
            raise Failure("hook directory is a symlink; manage the existing hook location manually")
        wanted = WRAPPER.read_bytes()
        if path.is_symlink() or (path.exists() and (not path.is_file() or path.read_bytes() != wanted)):
            raise Failure("existing pre-commit hook is not the unchanged Catalog hook; leave it intact and manage it manually")
        if install:
            self.prerequisites()
            self.policies()
            path.parent.mkdir(parents=True, exist_ok=True)
            if not path.exists():
                with path.open("xb") as output:
                    output.write(wanted)
            path.chmod(0o755)
            print("Catalog pre-commit hook installed; bypass explicitly with git commit --no-verify.")
        elif path.exists():
            path.unlink()
            print("Catalog pre-commit hook removed; other hooks and configuration unchanged.")
        else:
            print("Catalog pre-commit hook is not installed.")


def main(arguments):
    """Dispatch the explicit hook action and translate failures to safe diagnostics."""
    if len(arguments) != 1 or arguments[0] not in ("check", "install", "uninstall"):
        print("usage: python3 -B test/hooks/runner.py check|install|uninstall", file=sys.stderr)
        return 1
    try:
        checks = Checks()
        if arguments[0] == "check":
            checks.check()
        else:
            checks.hook(arguments[0] == "install")
    except (Failure, OSError) as error:
        message = str(error) if isinstance(error, Failure) else "cannot access hook files; check permissions and retry"
        print("pre-commit: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    # Interrupt a running child on SIGTERM as well as interactive Ctrl-C.
    def terminate(_signum, _frame):
        """Route SIGTERM through the same owned-child cleanup as an interrupt."""
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, terminate)
    sys.exit(main(sys.argv[1:]))
