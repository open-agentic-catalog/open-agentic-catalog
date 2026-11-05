"""Explicit real-scanner qualification; requires the documented pinned binary."""

import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import unittest
from unittest.mock import patch

import runner
import test_functional_runner as test_runner


class ScannerQualification(unittest.TestCase):
    def fixture(self):
        fixture = test_runner.HookTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        fixture.mock.stop()
        fixture.tool_mock.stop()
        repository = Path(__file__).resolve().parents[2]
        tools = {name.upper(): str(repository / relative) for name, relative in runner.TOOL_PATHS.items()
                 if (repository / relative).is_file()}
        environment = patch.dict(os.environ, tools)
        environment.start()
        self.addCleanup(environment.stop)
        return fixture

    def test_staged_data_and_ui_formatting_with_real_tools(self):
        fixture = self.fixture()
        fixture.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "data qualification baseline")
        elapsed = []
        def check():
            before = time.monotonic()
            try:
                fixture.check()
            finally:
                elapsed.append(time.monotonic() - before)

        cases = (
            ("data.json", b'{"value":1e999999}\n', b'{"private-key":1,"private-key":2}\n', "JSON"),
            ("data.jsonld", b'{"@context":{}}\n', b'{"value":NaN}\n', "JSON"),
            ("data with spaces.yml", b"value: staged\n", b"value: [private-invalid\n", "YAML"),
            ("line\nbreak.yaml", b"value: staged\n", b"value: 1\nvalue: 2\n", "YAML"),
            (":(glob)*.yaml", b"value: staged\n", b'"\xff"\n', "YAML"),
            ("api/openapi/control-v1.yaml", b"openapi: 3.1.0\n", b"value: [private-invalid\n", "YAML"),
        )
        for name, valid, invalid, kind in cases:
            with self.subTest(path=name):
                path = fixture.write(name, valid.decode())
                path.write_bytes(invalid)
                check()  # Invalid unstaged bytes cannot reject valid staged data.
                self.assertEqual(path.read_bytes(), invalid)
                fixture.git("add", "--", ":(literal)" + name)
                path.write_bytes(valid)
                with self.assertRaisesRegex(runner.Failure, kind) as failure:
                    check()
                self.assertNotIn("private", str(failure.exception))
                self.assertEqual(path.read_bytes(), valid)
                fixture.git("rm", "-f", "--", ":(literal)" + name)

        # Syntax-only upstream validation retains non-Catalog style, but rejects
        # duplicate keys. Raw Helm source remains a rendered-check obligation.
        upstream = fixture.write("schemas/upstream/profile/doc.yaml", "truth: yes\nlong: " + "x" * 300 + "\n")
        template = fixture.write("deployments/helm/open-agentic-catalog/templates/raw.yaml", "value: {{ .Values.value }}\n")
        check()
        fixture.write("schemas/upstream/profile/doc.yaml", "value: 1\nvalue: 2\n")
        with self.assertRaisesRegex(runner.Failure, "YAML"):
            check()
        fixture.git("rm", "-f", "--", str(upstream.relative_to(fixture.root)), str(template.relative_to(fixture.root)))

        ui = fixture.write("catalog-ui/internal/web/fixture.go", "package web\nvar value=1\n")
        ui.write_text("package web\n\nvar value = 1\n")
        with self.assertRaisesRegex(runner.Failure, "format or fix"):
            check()
        fixture.git("add", "catalog-ui/internal/web/fixture.go")
        check()

        # No YAML staged: a PATH with only the mandatory tools must still pass.
        tools = fixture.root / "isolated-tools"
        tools.mkdir()
        for name in ("git", "gofmt", "gitleaks"):
            (tools / name).symlink_to(Path(shutil.which(name)).resolve())
        with patch.dict(os.environ, {"PATH": str(tools)}):
            self.assertIsNone(shutil.which("yamllint"))
            check()
        print(f"Real staged checks: {len(elapsed)} runs, maximum {max(elapsed):.3f}s (30s budget).")

    def test_installed_hook_staged_secrets_allowlist_and_bypass(self):
        fixture = self.fixture()
        checks = runner.Checks(fixture.root)
        with contextlib.redirect_stdout(io.StringIO()):
            checks.hook(True)
        fixture.git("commit", "-qm", "clean initial commit through installed hook")
        # Deliberately synthetic, assembled so this test source is not itself a
        # stored token fixture or a scanner exception.
        secret = "ghp_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
        # Style exclusions must not become scanner exclusions, and inline
        # comments cannot replace reviewed fingerprint exceptions.
        for name in ("schemas/upstream/credential.txt", "internal/a2apb/credential.txt"):
            with self.subTest(path=name):
                fixture.write(name, 'github_token = "' + secret + '" # gitleaks:allow\n')
                try:
                    with self.assertRaisesRegex(runner.Failure, "possible staged secrets") as failure:
                        fixture.check()
                    self.assertNotIn(secret, str(failure.exception))
                finally:
                    # Remove only this newly staged synthetic fixture, even if
                    # the assertion fails, so cases cannot mask one another.
                    fixture.git("rm", "-f", "--", name)
        path = fixture.write("credentials with spaces.txt", 'github_token = "' + secret + '"\n')
        path.write_text("no credentials\n")
        with self.assertRaises(runner.Failure) as failure:
            fixture.check()
        self.assertIn("possible staged secrets", str(failure.exception))
        self.assertNotIn(secret, str(failure.exception))
        rejected = subprocess.run(["git", "commit", "-qm", "must fail"], cwd=fixture.root, capture_output=True)
        self.assertNotEqual(rejected.returncode, 0)
        self.assertNotIn(secret.encode(), rejected.stdout + rejected.stderr)
        # The same pin's exact finding fingerprint is the only exception.
        report = checks.run(["gitleaks", "git", "--staged", "--redact=100", "--no-banner", "--log-level", "error",
                             "--config", runner.POLICIES[0], "--report-format", "json", "--report-path", "/dev/stdout", "."])
        fingerprint = json.loads(report.stdout)[0]["Fingerprint"]
        fixture.write(runner.POLICIES[1], fingerprint + "\n")
        fixture.check()
        fixture.write(runner.POLICIES[1], "# No exceptions\n")
        fixture.git("add", "--", path.name)
        path.write_text('github_token = "' + secret + '"\n')
        fixture.check()  # Secret is now only in unstaged working-tree content.
        fixture.git("add", "--", path.name)
        fixture.git("commit", "--no-verify", "-qm", "explicit synthetic bypass")
        with contextlib.redirect_stdout(io.StringIO()):
            runner.Checks(fixture.root).hook(False)

    def test_each_added_linter_with_real_pinned_tools(self):
        fixture = self.fixture()
        fixture.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "linter baseline")
        workflow = ("name: Check\non: workflow_dispatch\njobs:\n  check:\n    runs-on: ubuntu-latest\n"
                    "    steps:\n      - run: printf 'ok\\n'\n")
        cases = (
            ("ruff", "test/staged.py", "value = 1\n", "import private_staged_marker\n"),
            ("shellcheck", "test/functional/restore.sh", "#!/bin/sh\nprintf 'ok\\n'\n",
             "#!/bin/sh\necho $private_staged_marker\n"),
            ("markdownlint", "notes [draft]\n-file.md", "# Title\n\nText.\n", "# Title\n\n#### private_staged_marker\n"),
            ("hadolint", "Containerfile", "FROM scratch\n", "FROM private-staged-marker\n"),
            ("actionlint", ".github/workflows/check.yml", workflow,
             workflow + "        if: $" + "{{ private_staged_marker.value }}\n"),
        )
        durations = []
        for tool, name, valid, invalid in cases:
            with self.subTest(tool=tool):
                before = time.monotonic()
                path = fixture.write(name, valid)
                path.write_text(invalid)
                fixture.check()
                self.assertEqual(path.read_text(), invalid)
                fixture.git("add", "--", ":(literal)" + name)
                path.write_text(valid)
                with self.assertRaisesRegex(runner.Failure, tool + " rejected staged") as failure:
                    fixture.check()
                self.assertNotIn("private_staged_marker", str(failure.exception))
                self.assertNotIn("private-staged-marker", str(failure.exception))
                self.assertEqual(path.read_text(), valid)
                fixture.git("rm", "-f", "--", ":(literal)" + name)
                durations.append(time.monotonic() - before)
        original_policy = (fixture.root / "ruff.toml").read_text()
        fixture.write("ruff.toml", original_policy.replace('extend-exclude = [', 'extend-exclude = ["test/excluded.py", ', 1))
        fixture.write("test/excluded.py", "import private_staged_marker\n")
        fixture.check()  # Relative Ruff exclusions retain the repository root.
        fixture.git("rm", "-f", "test/excluded.py")
        fixture.write("ruff.toml", original_policy)
        # Inline sources are not staged workflow content. actionlint's
        # ShellCheck integration must not follow its usual external-source flag.
        outside = fixture.write("unstaged-source.sh", "if\n", stage=False)
        workflow_path = fixture.write(".github/workflows/check.yml", workflow.replace("printf 'ok\\n'", "source " + str(outside)))
        # A pinned installation on PATH is supported without explicit command
        # variables or tools stored inside this disposable checkout.
        with patch.dict(os.environ):
            tools = runner.Checks(fixture.root)
            directory = str(Path(tools.executable("actionlint")).parent)
            os.environ["PATH"] = directory + os.pathsep + os.environ["PATH"]
            os.environ.pop("ACTIONLINT", None)
            os.environ.pop("SHELLCHECK", None)
            raw = subprocess.run([tools.executable("actionlint"), "-pyflakes=", "-shellcheck=" + tools.executable("shellcheck"),
                                  str(workflow_path)], cwd=fixture.root, capture_output=True, timeout=10)
            self.assertNotEqual(raw.returncode, 0, "external-source control did not reach ShellCheck")
            self.assertIn(b"shellcheck", raw.stdout + raw.stderr)
            fixture.check()
        print(f"Real pinned file linters: five passing/failing controls, maximum pair {max(durations):.3f}s.")

    def test_unselected_tools_are_not_required_with_real_path(self):
        fixture = self.fixture()
        fixture.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "prerequisite baseline")
        markdown = runner.Checks(fixture.root).executable("markdownlint")
        tools = fixture.root / "isolated-tools"
        tools.mkdir()
        for name in ("git", "gitleaks", "python3", "node"):
            (tools / name).symlink_to(Path(shutil.which(name)).resolve())
        with patch.dict(os.environ):
            os.environ["PATH"] = str(tools)
            for name in runner.TOOL_PATHS:
                os.environ.pop(name.upper(), None)
            self.assertIsNone(shutil.which("gofmt"))
            self.assertTrue(all(shutil.which(name) is None for name in runner.TOOL_PATHS))
            fixture.write("note.txt", "text\n")
            fixture.check()
            os.environ["MARKDOWNLINT"] = markdown
            fixture.write("README.md", "# Title\n")
            fixture.check()  # Markdown needs neither Go nor other file linters.
            fixture.write("test/fixture.py", "value = 1\n")
            with self.assertRaisesRegex(runner.Failure, "required tool unavailable.*ruff"):
                fixture.check()
        self.assertFalse(list(fixture.root.rglob(".ruff_cache")))

    def test_scanner_uses_verified_policies_after_working_edits(self):
        fixture = self.fixture()
        fixture.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "scanner snapshot baseline")
        secret = "ghp_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
        fixture.write("credentials.txt", 'github_token = "' + secret + '"\n')
        with self.assertRaisesRegex(runner.Failure, "possible staged secrets"):
            fixture.check()
        report = runner.Checks(fixture.root).run(
            ["gitleaks", "git", "--staged", "--redact=100", "--no-banner", "--log-level", "error",
             "--config", runner.POLICIES[0], "--report-format", "json", "--report-path", "/dev/stdout", "."])
        self.assertEqual(report.returncode, 1)
        fingerprint = json.loads(report.stdout)[0]["Fingerprint"]
        replacements = {
            runner.POLICIES[0]: 'title = "Changed working policy"\n[[rules]]\nid = "no-match"\nregex = "CATALOG_NEVER_MATCH_THIS_CONTROL"\n',
            runner.POLICIES[1]: fingerprint + "\n",
        }
        read_policy = runner.Checks.policy
        for name, replacement in replacements.items():
            with self.subTest(policy=name):
                original = (fixture.root / name).read_bytes()
                def policy(checks, selected, label):
                    raw = read_policy(checks, selected, label)
                    if selected == name:
                        (fixture.root / selected).write_text(replacement)
                    return raw
                try:
                    with patch.object(runner.Checks, "policy", policy):
                        with self.assertRaisesRegex(runner.Failure, "possible staged secrets") as failure:
                            fixture.check()
                    self.assertNotIn(secret, str(failure.exception))
                    self.assertEqual((fixture.root / name).read_text(), replacement)
                finally:
                    (fixture.root / name).write_bytes(original)


if __name__ == "__main__":
    unittest.main()
