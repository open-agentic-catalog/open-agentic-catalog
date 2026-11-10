"""Real Git/format checks with a deterministic scanner double, never host hooks."""

import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import runner


class HookTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="catalog-hook-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.environment = patch.dict(os.environ, {"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"})
        self.environment.start()
        self.addCleanup(self.environment.stop)
        # Git may export an alternate index to our parent test process.
        for name in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"):
            if name in os.environ:
                del os.environ[name]
        self.git("init", "-q")
        self.git("config", "user.name", "Hook Test")
        self.git("config", "user.email", "hook@example.invalid")
        repository = Path(__file__).resolve().parents[2]
        for name in (*runner.POLICIES, runner.DATA_VALIDATOR, runner.YAML_POLICY, runner.LINTER_PINS,
                     *runner.PYTHON_POLICIES, runner.MARKDOWN_POLICY, "test/hooks/pre-commit", "test/hooks/runner.py"):
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(repository / name, target)
        self.git("add", ".")
        self.scanner = subprocess.CompletedProcess([], 0, b"[]")
        self.yaml_result = subprocess.CompletedProcess([], 0, b"")
        self.yaml_calls = []
        self.linter_calls = []
        self.linter_snapshots = []
        self.linter_results = {}
        pins = json.loads((repository / runner.LINTER_PINS).read_text())
        self.versions = {"ruff": b"ruff 0.17.3", "actionlint": ("v" + pins["actionlint"]["version"]).encode(),
                         "shellcheck": ("version: " + pins["shellcheck"]).encode(),
                         "hadolint": ("Haskell Dockerfile Linter " + pins["hadolint"]).encode(),
                         "markdownlint": pins["markdownlint-cli"].encode()}
        self.version_status = {}
        original = runner.Checks.run

        def run(checks, arguments, data=None, **options):
            tool = Path(arguments[0]).name
            if tool in runner.TOOL_PATHS:
                if arguments[1] in ("--version", "-version"):
                    return subprocess.CompletedProcess(arguments, self.version_status.get(tool, 0), self.versions[tool])
                self.linter_calls.append((tool, arguments, data, options))
                directory = Path(options["directory"])
                self.linter_snapshots.append({str(path.relative_to(directory)): path.read_bytes()
                                              for path in directory.rglob("*")
                                              if path.is_file() and ".hook-policy" not in path.parts})
                return self.linter_results.get(tool, subprocess.CompletedProcess(arguments, 0, b"[]"))
            if arguments[0] == "yamllint":
                self.yaml_calls.append((arguments, data, Path(arguments[2]).read_bytes() if arguments[1] == "-c" else None))
                return self.yaml_result
            if arguments[0] == "gitleaks":
                if arguments[1] == "version":
                    return subprocess.CompletedProcess(arguments, 0, runner.GITLEAKS_VERSION.encode())
                self.assertIn("--staged", arguments)
                self.assertIn("--redact=100", arguments)
                self.assertIn("--ignore-gitleaks-allow", arguments)
                return self.scanner
            return original(checks, arguments, data, **options)

        self.mock = patch.object(runner.Checks, "run", run)
        self.mock.start()
        self.addCleanup(self.mock.stop)
        self.tool_mock = patch.object(runner.Checks, "executable", lambda _checks, name: name)
        self.tool_mock.start()
        self.addCleanup(self.tool_mock.stop)

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.root), *args], check=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout

    def write(self, name, text, stage=True):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        if stage:
            self.git("add", "--", ":(literal)" + name)
        return path

    def check(self):
        tree = self.git("write-tree")
        index = Path(os.fsdecode(self.git("rev-parse", "--git-path", "index")[:-1]))
        if not index.is_absolute():
            index = self.root / index
        before = index.read_bytes()
        with contextlib.redirect_stdout(io.StringIO()):
            try:
                runner.Checks(self.root).check()
            finally:
                self.assertEqual(index.read_bytes(), before)
                self.assertEqual(self.git("write-tree"), tree)

    def test_initial_partial_staging_and_safe_paths(self):
        for name in ("cmd/with spaces.go", "cmd/line\nbreak.go", "cmd/-dash.go", "catalog-ui/cmd/catalog-ui/fixture.go"):
            path = self.write(name, "package fixture\n\nvar value = 1\n")
            path.write_text("package fixture\nvar value=2\n")
            self.check()
            self.assertEqual(path.read_text(), "package fixture\nvar value=2\n")
        path = self.write("cmd/bad.go", "package fixture\nvar value=2\n")
        path.write_text("package fixture\n\nvar value = 2\n")
        with self.assertRaisesRegex(runner.Failure, "format or fix"):
            self.check()
        self.assertEqual(path.read_text(), "package fixture\n\nvar value = 2\n")

    def test_staged_ui_go_cannot_be_hidden_by_formatted_working_copy(self):
        path = self.write("catalog-ui/internal/web/fixture.go", "package web\nvar value=1\n")
        path.write_text("package web\n\nvar value = 1\n")
        with self.assertRaisesRegex(runner.Failure, "format or fix"):
            self.check()
        self.assertEqual(path.read_text(), "package web\n\nvar value = 1\n")

    def test_samples_formatting_and_non_go_prerequisites(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        self.write("note.txt", "text\n")
        original = runner.Checks.run
        def no_go(checks, arguments, data=None, **options):
            self.assertNotEqual(arguments[0], "gofmt", "non-Go changes require no Go tool")
            return original(checks, arguments, data, **options)
        with patch.object(runner.Checks, "run", no_go):
            self.check()
        path = self.write("samples/fixture.go", "package samples\nvar value=1\n")
        path.write_text("package samples\n\nvar value = 1\n")
        with self.assertRaisesRegex(runner.Failure, "format or fix"):
            self.check()

    def test_added_linters_use_staged_bytes_and_safe_diagnostics(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        cases = (
            ("ruff", "test/with spaces.py", b"value = 1\n",
             {"filename": "test/with spaces.py", "location": {"row": 1}, "code": "F401"}),
            ("shellcheck", "test/functional/restore.sh", b"#!/bin/sh\nprintf 'ok\\n'\n",
             {"file": "test/functional/restore.sh", "line": 2, "code": 2086}),
            ("markdownlint", "notes [draft]\n-file.md", b"# Title\n\nText.\n",
             {"fileName": "stdin", "lineNumber": 1, "ruleNames": ["MD001"]}),
            ("hadolint", "Containerfile", b"FROM scratch\n", {"file": "Containerfile", "line": 1, "code": "DL3006"}),
            ("actionlint", ".github/workflows/check.yml", b"name: test\non: workflow_dispatch\njobs: {}\n",
             {"filepath": ".github/workflows/check.yml", "line": 1, "kind": "expression"}),
        )
        for tool, name, source, finding in cases:
            with self.subTest(tool=tool):
                path = self.write(name, source.decode())
                path.write_text("private unstaged content\n")
                with patch.dict(os.environ, {"NODE_OPTIONS": "--private-option", "HADOLINT_IGNORE": "DL3006"}):
                    self.check()
                calls = [call for call in self.linter_calls if call[0] == tool]
                self.assertTrue(calls)
                call = calls[-1]
                self.assertNotIn("NODE_OPTIONS", call[3]["environment"])
                self.assertNotIn("HADOLINT_IGNORE", call[3]["environment"])
                if tool == "markdownlint":
                    self.assertEqual(call[2], source)
                else:
                    self.assertEqual(self.linter_snapshots[-1][name], source)
                finding["message"] = "private tool source"
                report = {"comments": [finding]} if tool == "shellcheck" else [finding]
                self.linter_results[tool] = subprocess.CompletedProcess([], 1, json.dumps(report).encode())
                with self.assertRaisesRegex(runner.Failure, tool + " rejected staged") as failure:
                    self.check()
                self.assertNotIn("private", str(failure.exception))
                self.assertIn('"line":', str(failure.exception))
                self.assertEqual(path.read_text(), "private unstaged content\n")
                self.linter_results[tool] = subprocess.CompletedProcess([], 2, b"private unstructured failure")
                with self.assertRaisesRegex(runner.Failure, tool + " failed") as failure:
                    self.check()
                self.assertNotIn("private", str(failure.exception))
                del self.linter_results[tool]
                self.git("rm", "-f", "--", ":(literal)" + name)

    def test_linter_policies_and_versions_cannot_drift(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        self.write("test/fixture.py", "value = 1\n")
        for name in runner.PYTHON_POLICIES:
            path = self.root / name
            original = path.read_bytes()
            path.write_text("private unstaged policy")
            with self.assertRaisesRegex(runner.Failure, "unstaged edits"):
                self.check()
            path.write_bytes(original)
        self.git("rm", "-f", "test/fixture.py")
        self.write("Containerfile", "FROM scratch\n")
        path = self.root / runner.LINTER_PINS
        original = path.read_bytes()
        path.write_text("{}\n")
        with self.assertRaisesRegex(runner.Failure, "unstaged edits"):
            self.check()
        path.write_bytes(original)
        self.versions["hadolint"] = b"0.0.0"
        with self.assertRaisesRegex(runner.Failure, "install the pinned hadolint"):
            self.check()

    def test_selected_tools_are_required_and_other_tools_are_unneeded(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        environment = self.enterContext(patch.dict(os.environ))
        for name in runner.TOOL_PATHS:
            environment.pop(name.upper(), None)
        cases = (("test/fixture.py", "value = 1\n", {"ruff"}),
                 ("test/functional/restore.sh", "#!/bin/sh\ntrue\n", {"shellcheck"}),
                 ("README.md", "# Title\n", {"markdownlint"}),
                 ("Containerfile", "FROM scratch\n", {"hadolint"}),
                 (".github/workflows/check.yml", "name: Check\non: workflow_dispatch\njobs: {}\n", {"actionlint", "shellcheck"}),
                 ("note.txt", "text\n", set()))
        self.tool_mock.stop()
        for name, source, needed in cases:
            self.write(name, source)
            called = []
            def which(tool):
                self.assertIn(tool, needed, "unselected tool was required")
                called.append(tool)
                return tool
            with patch.object(runner.shutil, "which", side_effect=which):
                self.check()
            self.assertEqual(set(called), needed)
            for missing in sorted(needed):
                with self.subTest(file=name, missing=missing), patch.object(
                        runner.shutil, "which", side_effect=lambda tool: None if tool == missing else tool):
                    with self.assertRaisesRegex(runner.Failure, "required tool unavailable.*" + missing):
                        self.check()
            self.git("rm", "-f", "--", ":(literal)" + name)

    def test_every_selected_linter_rejects_stale_versions(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        for tool, name, content in (
            ("ruff", "test/fixture.py", "value = 1\n"),
            ("shellcheck", "test/functional/restore.sh", "#!/bin/sh\ntrue\n"),
            ("markdownlint", "README.md", "# Title\n"),
            ("hadolint", "Containerfile", "FROM scratch\n"),
            ("actionlint", ".github/workflows/check.yml", "name: Check\non: workflow_dispatch\njobs: {}\n"),
        ):
            self.write(name, content)
            original = self.versions[tool]
            for invalid in (b"0.0.0 private version detail", b"\xff"):
                self.versions[tool] = invalid
                with self.assertRaisesRegex(runner.Failure, "install the pinned " + tool) as failure:
                    self.check()
                self.assertNotIn("private", str(failure.exception))
            self.versions[tool] = original
            self.version_status[tool] = 1
            with self.assertRaisesRegex(runner.Failure, "install the pinned " + tool):
                self.check()
            del self.version_status[tool]
            self.git("rm", "-f", "--", ":(literal)" + name)

    def test_malformed_and_inconsistent_linter_pins_fail_safely(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        self.write("Containerfile", "FROM scratch\n")
        pins = (self.root / runner.LINTER_PINS).read_text()
        for invalid in (None, [], {"hadolint": True}, {"hadolint": "private version"}, {"hadolint": "2.15"}):
            self.write(runner.LINTER_PINS, json.dumps(invalid) + "\n")
            with self.assertRaisesRegex(runner.Failure, "invalid or inconsistent") as failure:
                self.check()
            self.assertNotIn("private", str(failure.exception))
        self.write(runner.LINTER_PINS, pins)
        self.git("rm", "-f", "Containerfile")
        self.write("test/fixture.py", "value = 1\n")
        for name in runner.PYTHON_POLICIES:
            original = (self.root / name).read_text()
            # A different required version, dependency or locked package cannot
            # silently select the installed Ruff from another policy.
            invalid = original.replace("0.17.3", "0.0.0")
            for content in (invalid, "private invalid TOML = [\n"):
                self.write(name, content)
                with self.assertRaisesRegex(runner.Failure, "invalid or inconsistent") as failure:
                    self.check()
                self.assertNotIn("private", str(failure.exception))
            self.write(name, original)

    def test_used_file_linter_policies_reject_symlinks(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        self.write("test/fixture.py", "value = 1\n")
        self.write("README.md", "# Title\n")
        for name in (*runner.PYTHON_POLICIES, runner.LINTER_PINS, runner.MARKDOWN_POLICY):
            path = self.root / name
            original = path.read_bytes()
            target = self.write("policy-target", original.decode(), stage=False)
            path.unlink()
            path.symlink_to(target)
            try:
                with self.assertRaisesRegex(runner.Failure, "regular repository file"):
                    self.check()
                self.assertEqual(target.read_bytes(), original)
            finally:
                path.unlink()
                path.write_bytes(original)
        policy_parent = self.root / "test/ci"
        retained = self.root / "ci-owned"
        policy_parent.rename(retained)
        policy_parent.symlink_to(retained, target_is_directory=True)
        try:
            with self.assertRaisesRegex(runner.Failure, "regular repository file"):
                self.check()
        finally:
            policy_parent.unlink()
            retained.rename(policy_parent)

    def test_file_linter_policy_snapshots_survive_later_working_edits(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        self.write("test/fixture.py", "value = 1\n")
        self.write("README.md", "# Title\n")
        originals = {name: (self.root / name).read_bytes()
                     for name in (*runner.PYTHON_POLICIES, runner.LINTER_PINS, runner.MARKDOWN_POLICY)}
        read_policy = runner.Checks.policy
        observed = {}
        call = runner.Checks.run
        def policy(checks, name, label):
            raw = read_policy(checks, name, label)
            if name in originals:
                (self.root / name).write_text("private working edit after snapshot\n")
            return raw
        def run(checks, arguments, data=None, **options):
            if Path(arguments[0]).name in {"ruff", "markdownlint"} and "--config" in arguments:
                observed[Path(arguments[0]).name] = Path(arguments[arguments.index("--config") + 1]).read_bytes()
            return call(checks, arguments, data, **options)
        try:
            with patch.object(runner.Checks, "policy", policy), patch.object(runner.Checks, "run", run):
                self.check()
            self.assertEqual(observed, {"ruff": originals["ruff.toml"], "markdownlint": originals[runner.MARKDOWN_POLICY]})
            self.assertTrue(all((self.root / name).read_text() == "private working edit after snapshot\n" for name in originals))
        finally:
            for name, raw in originals.items():
                (self.root / name).write_bytes(raw)

    def test_scanner_policy_snapshots_survive_later_working_edits(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        self.write("note.txt", "text\n")
        originals = {name: (self.root / name).read_bytes() for name in runner.POLICIES}
        read_policy = runner.Checks.policy
        call = runner.Checks.run
        observed = {}
        directories = []
        def policy(checks, name, label):
            raw = read_policy(checks, name, label)
            if name in originals:
                (self.root / name).write_text("private working policy after snapshot\n")
            return raw
        def run(checks, arguments, data=None, **options):
            if arguments[:2] == ["gitleaks", "git"]:
                for name, flag in zip(runner.POLICIES, ("--config", "--gitleaks-ignore-path"), strict=True):
                    path = Path(arguments[arguments.index(flag) + 1])
                    if not path.is_absolute():
                        path = self.root / path
                    observed[name] = path.read_bytes()
                    directories.append(path.parent)
            return call(checks, arguments, data, **options)
        try:
            with patch.object(runner.Checks, "policy", policy), patch.object(runner.Checks, "run", run):
                self.check()
            self.assertEqual(observed, originals)
            self.assertTrue(all(directory != self.root / "test/hooks" and not directory.exists() for directory in directories))
            self.assertTrue(all((self.root / name).read_text() == "private working policy after snapshot\n" for name in originals))
        finally:
            for name, raw in originals.items():
                (self.root / name).write_bytes(raw)

    def test_file_linter_limits_cleanup_and_private_diagnostics(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
        self.write("test/fixture.py", "value = 1\n")
        doubled = runner.Checks.run
        self.mock.stop()
        native = runner.Checks.run
        for mode, message in (("deadline", "exceeded 30 seconds"), ("timeout", "timed out"), ("output", "output exceeds")):
            directories = []
            def run(checks, arguments, data=None, **options):
                if Path(arguments[0]).name == "ruff" and arguments[1] == "check":
                    directories.append(Path(options["directory"]))
                    if mode == "deadline":
                        checks.deadline = 0
                    elif mode == "timeout":
                        checks.deadline = runner.time.monotonic() + 0.05
                    script = "import time; time.sleep(10)" if mode == "timeout" else "import sys; sys.stderr.write('private source' * 100)"
                    with patch.object(runner, "OUTPUT_LIMIT", 64):
                        return native(checks, [sys.executable, "-c", script], **options)
                return doubled(checks, arguments, data, **options)
            with patch.object(runner.Checks, "run", run):
                with self.assertRaisesRegex(runner.Failure, message) as failure:
                    self.check()
            self.assertNotIn("private", str(failure.exception))
            self.assertEqual(len(directories), 1)
            self.assertFalse(directories[0].exists())
            self.assertFalse(list(self.root.rglob(".ruff_cache")))

    def test_json_partial_staging_safe_paths_and_strict_syntax(self):
        for name in ("data with spaces.json", "line\nbreak.jsonld", "-dash.json", ":(glob)*.json"):
            path = self.write(name, '{"value": 1e999999}\n')
            path.write_text("invalid working document")
            self.check()
            self.assertEqual(path.read_text(), "invalid working document")
        path = self.write("bad.json", "{}\n")
        for raw in (b'{"secret":1,"secret":2}\n', b'{"nested":{"a":1,"a":2}}\n',
                    b'{"x":NaN}\n', b'{} {}\n', b'"\xff"\n', b'{"sensitive":}\n'):
            path.write_bytes(raw)
            self.git("add", "bad.json")
            path.write_text("{}\n")
            with self.assertRaisesRegex(runner.Failure, "invalid staged JSON") as failure:
                self.check()
            self.assertNotIn("secret", str(failure.exception))
            self.assertNotIn("sensitive", str(failure.exception))
            self.assertEqual(path.read_bytes(), b"{}\n")

    def test_yaml_uses_index_bytes_and_staged_policy_with_correct_scope(self):
        path = self.write("api/openapi/control-v1.yaml", "value: staged\n")
        path.write_text("value: [broken working YAML\n")
        self.check()
        self.assertIn(b"value: staged\n", [call[1] for call in self.yaml_calls])
        self.assertTrue(all(call[2] == (self.root / runner.YAML_POLICY).read_bytes() for call in self.yaml_calls))
        self.assertEqual(path.read_text(), "value: [broken working YAML\n")
        self.write("schemas/upstream/profile/document.yaml", "truth: yes\n")
        self.write("deployments/helm/open-agentic-catalog/templates/raw.yaml", "{{ invalid: {{ template }}\n")
        self.write("deployments/helm/open-agentic-catalog-monitoring/templates/raw.yaml", "{{ invalid: {{ template }}\n")
        self.yaml_calls.clear()
        self.check()
        upstream = [call for call in self.yaml_calls if call[1] == b"truth: yes\n"]
        self.assertEqual(len(upstream), 1)
        self.assertEqual(upstream[0][0][1:3], ["-d", '{"rules": {"key-duplicates": "enable"}}'])
        self.assertFalse(any(b"template" in call[1] for call in self.yaml_calls))

    def test_data_policy_drift_and_links_are_rejected(self):
        self.write("value.json", "{}\n")
        for name in (runner.DATA_VALIDATOR, runner.YAML_POLICY):
            path = self.root / name
            original = path.read_bytes()
            path.write_text("unstaged validator or policy\n")
            with self.assertRaisesRegex(runner.Failure, "unstaged edits"):
                self.check()
            path.write_bytes(original)
            path.unlink()
            target = self.write("policy-source", original.decode(), stage=False)
            path.symlink_to(target)
            with self.assertRaisesRegex(runner.Failure, "regular repository file"):
                self.check()
            path.unlink()
            path.write_bytes(original)

    def test_data_links_and_submodules_fail_closed(self):
        for name in ("link.json", "link.jsonld", "link.yaml", "link.yml", "deployments/helm/open-agentic-catalog/templates/link.yaml"):
            target = self.write("private-data", "{}\n", stage=False)
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.symlink_to(target)
            self.git("add", "--", name)
            with self.assertRaisesRegex(runner.Failure, "staged file must be regular"):
                self.check()
            self.assertEqual(target.read_text(), "{}\n")
            self.git("rm", "-f", "--", name)
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
        commit = self.git("rev-parse", "HEAD").strip().decode()
        self.git("update-index", "--add", "--cacheinfo", "160000," + commit + ",submodule.json")
        with self.assertRaisesRegex(runner.Failure, "staged file must be regular"):
            self.check()

    def test_data_rename_deletion_and_no_yaml_tool_requirement(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
        self.yaml_calls.clear()
        self.write("old.txt", "invalid JSON\n")
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "text fixture")
        self.git("mv", "old.txt", "new.json")
        with self.assertRaisesRegex(runner.Failure, "invalid staged JSON"):
            self.check()
        self.git("rm", "-f", "new.json")
        self.write("only.json", "{}\n")
        self.check()
        self.assertEqual(self.yaml_calls, [])

    def test_data_validation_uses_verified_snapshots_after_policy_reads(self):
        self.write("value.json", "{}\n")
        checks = runner.Checks(self.root)
        original = checks.policy
        originals = {name: (self.root / name).read_bytes() for name in (runner.DATA_VALIDATOR, runner.YAML_POLICY)}
        def policy(name, label):
            raw = original(name, label)
            (self.root / name).write_text("invalid working content after snapshot\n")
            return raw
        try:
            with patch.object(checks, "policy", side_effect=policy):
                checks.data(b"value.json\0.yamllint.yml\0")
            self.assertEqual(self.yaml_calls[-1][2], originals[runner.YAML_POLICY])
            self.assertEqual(self.yaml_calls[-1][1], originals[runner.YAML_POLICY])
        finally:
            for name, raw in originals.items():
                (self.root / name).write_bytes(raw)

    def test_yaml_tool_failure_is_private_and_uses_shared_budget(self):
        self.yaml_result = subprocess.CompletedProcess([], 2, b"sensitive validator detail")
        with self.assertRaisesRegex(runner.Failure, "staged YAML validation failed") as failure:
            self.check()
        self.assertNotIn("sensitive", str(failure.exception))
        checks = runner.Checks(self.root)
        self.mock.stop()
        original = checks.run
        def run(arguments, data=None):
            if arguments[0] == "yamllint":
                checks.deadline = 0
            return original(arguments, data)
        with patch.object(checks, "run", side_effect=run):
            with self.assertRaisesRegex(runner.Failure, "exceeded 30 seconds"):
                checks.data(b".yamllint.yml\0")

    def test_data_validator_snapshot_failure_and_staged_policy_mode(self):
        self.write(runner.DATA_VALIDATOR, 'raise RuntimeError("private validator details")\n')
        with self.assertRaisesRegex(runner.Failure, "validator could not be loaded") as failure:
            self.check()
        self.assertNotIn("private", str(failure.exception))
        self.write(runner.DATA_VALIDATOR, "# Missing parser entry points\n")
        with self.assertRaisesRegex(runner.Failure, "validator could not be loaded"):
            self.check()
        self.write("value.json", "{}\n")
        for parser, scope in (("raise RuntimeError('private parser details')", "return 'maintained'"),
                              ("pass", "raise RuntimeError('private scope details')"),
                              ("pass", "return 'invalid-scope'")):
            self.write(runner.DATA_VALIDATOR, f"def validate_json(raw):\n    {parser}\ndef yaml_scope(name):\n    {scope}\n")
            with self.assertRaisesRegex(runner.Failure, "staged data validator failed") as failure:
                self.check()
            self.assertNotIn("private", str(failure.exception))
        target = self.write("validator-target", "{}\n", stage=False)
        path = self.root / runner.DATA_VALIDATOR
        path.unlink()
        path.symlink_to(target)
        self.git("add", runner.DATA_VALIDATOR)
        path.unlink()
        path.write_text(str(target))
        # A regular worktree path does not make a staged symlink regular.
        with self.assertRaisesRegex(runner.Failure, "staged file must be regular"):
            self.check()

    def test_missing_yaml_tool_and_oversized_data_fail_safely(self):
        checks = runner.Checks(self.root)
        self.mock.stop()
        original = checks.run
        def run(arguments, data=None):
            if arguments[0] == "yamllint":
                arguments = ["/nonexistent/catalog-yaml-tool"]
            return original(arguments, data)
        with patch.object(checks, "run", side_effect=run):
            with self.assertRaisesRegex(runner.Failure, "required tool unavailable"):
                checks.data(b".yamllint.yml\0")
        self.write("large.json", '"' + 'x' * 1024 + '"\n')
        with patch.object(runner, "OUTPUT_LIMIT", 512):
            with self.assertRaisesRegex(runner.Failure, "output exceeds"):
                checks.blob("large.json")

    def test_whitespace_conflicts_and_invalid_go(self):
        for content in ("text \n", "<<<<<<< ours\n=======\n>>>>>>> theirs\n"):
            self.write("file.txt", content)
            with self.assertRaisesRegex(runner.Failure, "whitespace"):
                self.check()
        self.write("file.txt", "text\n")
        self.write("cmd/bad.go", "sensitive invalid Go")
        with self.assertRaises(runner.Failure) as result:
            self.check()
        self.assertNotIn("sensitive", str(result.exception))

    def test_go_fragment_is_not_a_source_file(self):
        self.write("cmd/fragment.go", "var value = 1\n")
        with self.assertRaisesRegex(runner.Failure, "format or fix"):
            self.check()

    def test_repository_path_ending_in_newline(self):
        nested = self.root / "repository\n"
        nested.mkdir()
        subprocess.run(["git", "init", "-q", str(nested)], check=True)
        self.assertEqual(runner.Checks(nested).root, nested)

    def test_deletion_rename_empty_and_symlink(self):
        self.write("cmd/old.go", "package fixture\n")
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
        with patch.object(runner.Checks, "prerequisites", side_effect=AssertionError("empty check needs no scanner")), \
                patch.object(runner.Checks, "policies", side_effect=AssertionError("empty check needs no policy")):
            self.check()
        self.git("mv", "cmd/old.go", "cmd/new.go")
        self.check()
        self.git("rm", "-f", "cmd/new.go")
        target = self.write("private.txt", "not Go source", stage=False)
        (self.root / "cmd").mkdir(exist_ok=True)
        (self.root / "cmd/link.go").symlink_to(target)
        self.git("add", "cmd/link.go")
        self.check()

    def test_style_exclusions_and_executable(self):
        self.write("schemas/upstream/fixture.go", "not Go\n")
        self.write("internal/a2apb/fixture.go", "not Go\n")
        self.write("catalog-ui/node_modules/tool/fixture.go", "not Go\n")
        path = self.write("cmd/executable.go", "package fixture\n")
        path.chmod(0o755)
        self.git("add", "cmd/executable.go")
        self.check()

    def test_scanner_failures_and_safe_findings(self):
        for status, content in ((2, b"private tool stderr"), (1, b"not JSON"), (1, b"[]"), (1, b"{}"), (1, b"\xff")):
            self.scanner = subprocess.CompletedProcess([], status, content)
            with self.assertRaisesRegex(runner.Failure, "scanner failed") as result:
                self.check()
            self.assertNotIn("private", str(result.exception))
        self.scanner = subprocess.CompletedProcess([], 1, b'[{"File":"bad\\u001bfile","RuleID":"test","StartLine":2,"Secret":"private","Match":"private"}]')
        with self.assertRaises(runner.Failure) as result:
            self.check()
        self.assertIn("bad\\u001bfile", str(result.exception))
        self.assertNotIn("private", str(result.exception))
        self.assertNotIn("\x1b", str(result.exception))

    def test_findings_are_bounded_and_malformed_entries_fail_closed(self):
        self.scanner = subprocess.CompletedProcess([], 1, b'["private"]')
        with self.assertRaisesRegex(runner.Failure, "invalid findings"):
            self.check()
        finding = {"File": "x" * 2000, "RuleID": "fixture", "StartLine": 1, "Secret": "private"}
        self.scanner = subprocess.CompletedProcess([], 1, json.dumps([finding] * 20).encode())
        with self.assertRaises(runner.Failure) as result:
            self.check()
        lines = str(result.exception).splitlines()
        self.assertEqual(len(lines), 11)  # Summary and at most ten locations.
        self.assertTrue(all(len(line) <= 1024 for line in lines[1:]))
        self.assertNotIn("private", str(result.exception))

    def test_policy_must_match_index(self):
        for name in runner.POLICIES:
            with self.subTest(policy=name):
                path = self.root / name
                original = path.read_bytes()
                path.write_text("unstaged policy")
                with self.assertRaisesRegex(runner.Failure, "unstaged edits"):
                    self.check()
                path.write_bytes(original)

    def test_missing_symlinked_and_oversized_policies_are_rejected(self):
        for name in runner.POLICIES:
            with self.subTest(policy=name):
                path = self.root / name
                original = path.read_bytes()
                path.unlink()
                with self.assertRaisesRegex(runner.Failure, "policy missing"):
                    runner.Checks(self.root).policies()
                target = self.write("policy-target", original.decode(), stage=False)
                path.symlink_to(target)
                with self.assertRaisesRegex(runner.Failure, "regular repository file"):
                    runner.Checks(self.root).policies()
                self.assertEqual(target.read_bytes(), original)
                path.unlink()
                path.write_bytes(original)
                checks = runner.Checks(self.root)
                # Exercise the real size check without allocating an 8 MiB fixture.
                limit = max((self.root / policy).stat().st_size for policy in runner.POLICIES) + 1
                path.write_bytes(b"x" * (limit + 1))
                with patch.object(runner, "OUTPUT_LIMIT", limit):
                    with self.assertRaisesRegex(runner.Failure, "regular repository file"):
                        checks.policies()
                path.write_bytes(original)

    def test_unmerged_index_is_rejected_without_changing_it(self):
        blob = self.git("rev-parse", ":" + runner.POLICIES[0]).strip()
        entries = b"".join(b"100644 " + blob + b" " + str(stage).encode() + b"\tconflicted.txt\n" for stage in (1, 2, 3))
        subprocess.run(["git", "update-index", "--index-info"], cwd=self.root, input=entries, check=True)
        before = (self.root / ".git/index").read_bytes()
        with self.assertRaisesRegex(runner.Failure, "resolve staged merge conflicts"):
            runner.Checks(self.root).check()
        self.assertEqual((self.root / ".git/index").read_bytes(), before)

    def test_install_uninstall_preserves_other_hooks(self):
        checks = runner.Checks(self.root)
        hook = self.root / ".git/hooks/pre-commit"
        other = self.root / ".git/hooks/post-commit"
        other.write_text("unrelated")
        with contextlib.redirect_stdout(io.StringIO()):
            checks.hook(True)
            checks.hook(True)
            self.assertTrue(os.access(hook, os.X_OK))
            hook.write_text("unmanaged")
            for install in (True, False):
                with self.assertRaisesRegex(runner.Failure, "existing pre-commit"):
                    checks.hook(install)
            self.assertEqual(hook.read_text(), "unmanaged")
            hook.write_bytes(runner.WRAPPER.read_bytes())
            with patch.object(checks, "prerequisites", side_effect=AssertionError("uninstall needs no tools")), \
                    patch.object(checks, "policies", side_effect=AssertionError("uninstall needs no policy")):
                checks.hook(False)
                checks.hook(False)
        self.assertFalse(hook.exists())
        self.assertEqual(other.read_text(), "unrelated")
        self.git("config", "core.hooksPath", "custom")
        with self.assertRaisesRegex(runner.Failure, "custom core.hooksPath"):
            checks.hook(True)
        self.assertEqual(self.git("config", "--get", "core.hooksPath").strip(), b"custom")

    def test_alternate_index_and_linked_worktree(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
        original = (self.root / ".git/index").read_bytes()
        alternate = self.root / "alternate-index"
        shutil.copyfile(self.root / ".git/index", alternate)
        with patch.dict(os.environ, {"GIT_INDEX_FILE": str(alternate)}):
            self.write("cmd/bad.go", "not Go")
            with self.assertRaisesRegex(runner.Failure, "format or fix"):
                self.check()
            self.git("rm", "-f", "cmd/bad.go")
            path = self.write("data.json", '{"a":1,"a":2}\n')
            path.write_text("{}\n")
            with self.assertRaisesRegex(runner.Failure, "invalid staged JSON"):
                self.check()
        self.assertEqual((self.root / ".git/index").read_bytes(), original)
        worktree = self.root / "linked"
        self.git("worktree", "add", "--detach", str(worktree))
        checks = runner.Checks(worktree)
        with contextlib.redirect_stdout(io.StringIO()):
            checks.hook(True)
            self.assertTrue((self.root / ".git/hooks/pre-commit").is_file())
            checks.hook(False)
            path = worktree / "data.yaml"
            path.write_text("value: staged\n")
            subprocess.run(["git", "-C", str(worktree), "add", "data.yaml"], check=True)
            path.write_text("invalid: [working\n")
            checks.check()
            self.assertEqual(self.yaml_calls[-1][1], b"value: staged\n")

    def test_missing_tool_deadline_and_process_failure(self):
        checks = runner.Checks(self.root)
        with self.assertRaisesRegex(runner.Failure, "required tool"):
            checks.run(["/nonexistent/catalog-hook-tool"])
        checks.deadline = 0
        with self.assertRaisesRegex(runner.Failure, "exceeded"):
            checks.run(["git", "version"])
        checks.deadline = runner.time.monotonic() + 0.1
        with self.assertRaisesRegex(runner.Failure, "timed out"):
            checks.run(["python3", "-c", "import time; time.sleep(10)"])

    def test_symlinked_hook_directory_and_hook_are_not_modified(self):
        checks = runner.Checks(self.root)
        directory = self.root / ".git/hooks"
        moved = self.root / "shared-hooks"
        directory.rename(moved)
        directory.symlink_to(moved, target_is_directory=True)
        with self.assertRaisesRegex(runner.Failure, "directory is a symlink"):
            checks.hook(True)
        self.assertFalse((moved / "pre-commit").exists())
        directory.unlink()
        moved.rename(directory)
        target = self.write("unmanaged-hook", "unrelated", stage=False)
        (directory / "pre-commit").symlink_to(target)
        for install in (True, False):
            with self.assertRaisesRegex(runner.Failure, "existing pre-commit"):
                checks.hook(install)
        self.assertEqual(target.read_text(), "unrelated")

    def test_incompatible_scanner_and_large_output(self):
        checks = runner.Checks(self.root)
        with patch.object(checks, "run", return_value=subprocess.CompletedProcess([], 0, b"0.0.0")):
            with self.assertRaisesRegex(runner.Failure, "install the documented Gitleaks"):
                checks.prerequisites()
        with patch.object(runner, "OUTPUT_LIMIT", 10):
            with self.assertRaisesRegex(runner.Failure, "output exceeds"):
                checks.run(["python3", "-c", "print('x' * 20)"])

    def test_make_targets_do_not_read_env(self):
        source = Path(__file__).resolve().parents[2]
        self.write("Makefile", (source / "Makefile").read_text(), stage=False)
        self.write(".env", "$(error must not load env)\n", stage=False)
        for target in ("hooks-install", "hooks-uninstall", "pre-commit-check"):
            for selected in ([], ["CATALOG_ENV_FILE=.env"]):
                result = subprocess.run(["make", "-n", target, *selected], cwd=self.root, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
        result = subprocess.run(["make", "-n", "hooks-install", "help"], cwd=self.root, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(b"must not load env", result.stderr)

    def test_command_line_usage_and_empty_check(self):
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
        for arguments, status, message in (([], 1, b"usage:"), (["unknown"], 1, b"usage:"),
                                           (["check", "extra"], 1, b"usage:"), (["check"], 0, b"No staged changes")):
            with self.subTest(arguments=arguments):
                result = subprocess.run([sys.executable, "-B", "test/hooks/runner.py", *arguments],
                                        cwd=self.root, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, status)
                self.assertIn(message, result.stdout + result.stderr)
                self.assertNotIn(b"Traceback", result.stderr)

    def test_entrypoint_errors_do_not_disclose_os_error_details(self):
        for error, message in ((runner.Failure("safe diagnostic"), "safe diagnostic"),
                               (OSError("private path or tool detail"), "cannot access hook files")):
            with self.subTest(error=type(error).__name__):
                output = io.StringIO()
                with patch.object(runner, "Checks", side_effect=error), contextlib.redirect_stderr(output):
                    self.assertEqual(runner.main(["check"]), 1)
                self.assertIn(message, output.getvalue())
                self.assertNotIn("private", output.getvalue())


if __name__ == "__main__":
    unittest.main()
