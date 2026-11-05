"""Owned Git, Make and process fixtures; no full workflow jobs or deployments."""

from contextlib import nullcontext, redirect_stdout
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import textwrap
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import local_workflows as local


class LocalWorkflowFixtures(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog-act-fixture-")
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.root = self.base / "source with spaces"
        self.root.mkdir()
        self.env = dict(os.environ, GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1", GIT_TERMINAL_PROMPT="0")
        self.git("init", "--quiet")
        self.git("config", "user.name", "Local fixture")
        self.git("config", "user.email", "fixture@example.test")
        self.git("config", "commit.gpgsign", "false")
        (self.root / ".gitignore").write_text(".tools/\noutput/\nprivate.env\n")
        (self.root / "VERSION").write_text("0.1.0\n")
        workflows = self.root / ".github/workflows"
        shutil.copytree(local.ROOT / ".github/workflows", workflows)
        self.git("add", ".")
        self.git("commit", "--quiet", "-m", "test: create local workflow fixture")
        self.commit = self.git("rev-parse", "HEAD").strip()
        self.owned = self.root / ".tools/ci/act"
        self.owned.mkdir(parents=True)
        self.observe = self.base / "observation.json"
        script = "#!" + sys.executable + "\n" + '''import json, os, pathlib, subprocess, sys
if '--version' in sys.argv:
    print('act version 0.2.89')
else:
    source = pathlib.Path(sys.argv[sys.argv.index('--directory') + 1])
    observed = {'args': sys.argv[1:], 'environment': dict(os.environ),
                'source': str(source), 'files': sorted(p.name for p in source.iterdir()),
                'config': (source / '.git/config').read_text(),
                'head': subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip()}
    pathlib.Path(OBSERVE).write_text(json.dumps(observed))
    (source / 'generated-only-in-disposable-copy').write_text('disposable')
    sys.exit(EXIT_STATUS)
'''.replace("OBSERVE", repr(str(self.observe))).replace("EXIT_STATUS", "7")
        executable = self.owned / "act"
        executable.write_text(script)
        executable.chmod(0o755)
        (self.owned / "installation.json").write_text(json.dumps({
            "version": local.PIN["version"], "archiveSha256": local.PIN["sha256"],
            "binarySha256": hashlib.sha256(executable.read_bytes()).hexdigest(),
        }))

    def git(self, *arguments):
        return subprocess.run(["git", "-C", str(self.root), *arguments], env=self.env,
                              check=True, capture_output=True, text=True, timeout=30).stdout

    def test_real_git_copy_retains_identity_tags_and_history_without_private_inputs(self):
        self.git("tag", "v0.1.0")
        self.git("remote", "add", "private-source", "https://private.invalid/source")
        self.git("config", "credential.helper", "private-credential-helper")
        sentinel = self.base / "hook-ran"
        hook = self.root / ".git/hooks/post-checkout"
        hook.write_text("#!/bin/sh\ntouch " + str(sentinel) + "\n")
        hook.chmod(0o755)
        (self.root / "private.env").write_text("PRIVATE-SENTINEL")
        with local.private_directory() as (directory, env):
            destination = directory / "source"
            local.copy_source(self.root, destination, self.commit, env)
            self.assertEqual(local.source_identity(destination, env), self.commit)
            self.assertEqual(local.git(destination, "tag", env=env).strip(), "v0.1.0")
            self.assertEqual(local.git(destination, "remote", env=env), "")
            self.assertNotIn("private-credential-helper", (destination / ".git/config").read_text())
            self.assertFalse((destination / ".git/hooks").exists())
            self.assertFalse((destination / "private.env").exists())
            self.assertFalse((destination / ".tools").exists())
            self.assertFalse(sentinel.exists())
        self.assertFalse(directory.exists())
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_real_process_receives_isolated_source_and_environment_and_retains_failure(self):
        (self.root / "private.env").write_text("PRIVATE-SENTINEL")
        original_index = (self.root / ".git/index").read_bytes()
        def docker(arguments, *_args, **_kwargs):
            self.assertEqual(arguments, ["ps", "-a", "--format", "{{.Names}}"])
            return SimpleNamespace(stdout="")
        with (
            patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned),
            patch.object(local, "check_runner", return_value="sha256:fixture"),
            patch.object(local, "docker", side_effect=docker),
            patch.object(local, "docker_resources", return_value=nullcontext()),
            patch.dict(os.environ, {"GH_TOKEN": "PRIVATE-SENTINEL", "ACTIONS_RUNTIME_URL": "https://private.invalid",
                                    "GOTELEMETRY": "on", "HOME": "/private/home"}),
            redirect_stdout(io.StringIO()),
        ):
            self.assertEqual(local.run("docs", "backend"), 7)
        observed = json.loads(self.observe.read_text())
        self.assertEqual(observed["head"], self.commit)
        self.assertNotIn("PRIVATE-SENTINEL", json.dumps(observed))
        self.assertEqual(observed["environment"]["GOTELEMETRY"], "off")
        self.assertNotIn("GH_TOKEN", observed["environment"])
        self.assertNotIn("ACTIONS_RUNTIME_URL", observed["environment"])
        self.assertIn("upload_artifacts=false", observed["args"])
        self.assertFalse(Path(observed["source"]).parent.exists())
        self.assertEqual((self.root / ".git/index").read_bytes(), original_index)
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertFalse((self.root / "generated-only-in-disposable-copy").exists())

    def test_copy_materializes_objects_borrowed_by_the_original_checkout(self):
        borrowed = self.base / "borrowed objects"
        subprocess.run(["git", "clone", "--quiet", "--shared", "--", str(self.root), str(borrowed)],
                       env=self.env, check=True, capture_output=True, text=True, timeout=30)
        self.assertTrue((borrowed / ".git/objects/info/alternates").is_file())
        with local.private_directory() as (directory, env):
            destination = directory / "source"
            local.copy_source(borrowed, destination, self.commit, env)
            self.assertFalse((destination / ".git/objects/info/alternates").exists())
            self.assertEqual(local.git(destination, "show", "HEAD:VERSION", env=env), "0.1.0\n")

    def test_each_kind_of_uncommitted_input_refuses_before_docker_or_execution(self):
        for kind in ("unstaged", "staged", "untracked"):
            with self.subTest(kind=kind):
                target = self.root / ("VERSION" if kind != "untracked" else "untracked-file")
                target.write_text("changed\n")
                if kind == "staged":
                    self.git("add", "VERSION")
                with local.private_directory() as (_directory, env), self.assertRaisesRegex(ValueError, "commit staged"):
                    local.source_identity(self.root, env)
                if kind == "untracked":
                    target.unlink()
                else:
                    self.git("restore", "--staged", "VERSION")
                    self.git("restore", "VERSION")
        self.assertFalse(self.observe.exists())

    def test_preexisting_act_container_refuses_without_copying_source_or_running_job(self):
        with (
            patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned),
            patch.object(local, "check_runner"), patch.object(local, "docker", return_value=SimpleNamespace(stdout="act-existing-job\n")),
            patch.object(local, "copy_source") as copy,
        ):
            with self.assertRaisesRegex(ValueError, "pre-existing act containers"):
                local.run("docs", "backend")
            copy.assert_not_called()
        self.assertFalse(self.observe.exists())

    def test_make_bypasses_poisoned_env_and_lists_without_tools_or_docker(self):
        shutil.copyfile(local.ROOT / "Makefile", self.root / "Makefile")
        shutil.copyfile(local.ROOT / "go.mod", self.root / "go.mod")
        shutil.copytree(local.ROOT / "test/ci", self.root / "test/ci", ignore=shutil.ignore_patterns("__pycache__"))
        (self.root / "private.env").write_text("$(error Loaded private deployment settings)\n")
        before = self.git("status", "--porcelain")
        result = subprocess.run(["make", "--no-print-directory", "CATALOG_ENV_FILE=private.env", "workflow-local-list"],
                                cwd=self.root, env=self.env, capture_output=True, text=True, timeout=20, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in ("lint", "unit", "ui", "docs", "charts", "image", "security", "postgres"):
            self.assertIn("workflow-local-" + name + ":", result.stdout)
        self.assertEqual(self.git("status", "--porcelain"), before)
        for target in ("tools-install", "list", "lint", "unit", "ui", "docs", "charts", "image", "security", "postgres"):
            result = subprocess.run(["make", "--no-print-directory", "CATALOG_ENV_FILE=private.env",
                                     "workflow-local-" + target, "help"], cwd=self.root, env=self.env,
                                    capture_output=True, text=True, timeout=10, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Local workflow targets must run alone", result.stderr)
            self.assertNotIn("Loaded private", result.stderr)
        sentinel = self.base / "make-expanded-untrusted-product"
        for variable, expected in (("WORKFLOW_LOCAL_PRODUCT", "select a fixed local workflow"),
                                   ("WORKFLOW_LOCAL_SCAN", "require exactly true or false"),
                                   ("WORKFLOW_LOCAL_RELEASE_CANDIDATE", "require exactly true or false")):
            env = dict(self.env, **{variable: "$(shell touch " + str(sentinel) + ")"})
            result = subprocess.run(["make", "--no-print-directory", "workflow-local-image"], cwd=self.root,
                                    env=env, capture_output=True, text=True, timeout=10, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(expected, result.stderr)
            self.assertFalse(sentinel.exists())

    def test_functional_prerequisites_and_commands_are_closed_and_preserve_failures(self):
        source = (local.ROOT / ".github/workflows/functional.yml").read_text()
        steps = re.split(r"(?m)^      - ", source)
        prepare = next(step for step in steps if step.startswith("name: Select fixed prerequisites and check runner capacity\n"))
        dispatch = next(step for step in steps if "id: qualification\n" in step)
        scripts = [textwrap.dedent(re.search(r"(?ms)^        run: \|\n(.*)", step)[1]) for step in (prepare, dispatch)]
        cases = {
            "coverage-postgres-check": ("functional", ["coverage-postgres-check"]),
            "test-process-postgres": ("functional", ["test-process-postgres"]),
            "test-functional": ("functional", ["-j1", "test-functional"]),
            "test-ui-compose": ("functional-ui", ["test-ui-compose"]),
            "test-ui-oidc": ("functional-ui", ["test-ui-oidc"]),
            "test-backup-restore": ("functional", ["test-backup-restore"]),
            "test-federation-peers": ("functional-ui", ["test-federation-peers"]),
            "test-webhook-report": ("functional", ["test-webhook-report"]),
            "all": (None, None), "test-fuzz": (None, None), "test-webhook-report; exit 0": (None, None),
        }
        for index, (suite, (profile, command)) in enumerate(cases.items()):
            for preflight_status, target_status in ((0, 0), (7, 0), (0, 7)):
                with self.subTest(suite=suite, preflight_status=preflight_status, target_status=target_status):
                    directory = self.base / f"functional-dispatch-{index}-{preflight_status}-{target_status}"
                    directory.mkdir()
                    observed, exported = directory / "commands.jsonl", directory / "env"
                    for name in ("python3.12", "make"):
                        executable = directory / name
                        executable.write_text("#!" + sys.executable + "\n" + '''import json, os, pathlib, sys
with pathlib.Path(os.environ["OBSERVED_COMMANDS"]).open("a") as output:
    output.write(json.dumps(sys.argv[1:]) + "\\n")
sys.exit(int(os.environ["CONTROL_EXIT"]))
''')
                        executable.chmod(0o755)
                    env = {**local.environment(directory), "SUITE": suite, "CONTROL_EXIT": str(preflight_status),
                           "OBSERVED_COMMANDS": str(observed), "GITHUB_ENV": str(exported)}
                    env["PATH"] = str(directory) + os.pathsep + env["PATH"]
                    prepared = subprocess.run(["bash", "--noprofile", "--norc", "-euo", "pipefail", "-c", scripts[0]],
                                              cwd=self.root, env=env, capture_output=True, text=True, timeout=15, check=False)
                    expected_status = 2 if profile is None else preflight_status
                    self.assertEqual(prepared.returncode, expected_status, prepared.stdout + prepared.stderr)
                    if profile is not None and preflight_status:
                        self.assertFalse(exported.exists())
                        self.assertEqual([json.loads(line) for line in observed.read_text().splitlines()],
                                         [["-B", "test/ci/tools.py", "preflight", "--profile", profile]])
                        continue
                    if profile is not None:
                        self.assertEqual(exported.read_text(), "CI_TOOL_PROFILE=" + profile + "\n")
                    else:
                        self.assertFalse(exported.exists())
                    env["CONTROL_EXIT"] = str(target_status)
                    completed = subprocess.run(["bash", "--noprofile", "--norc", "-euo", "pipefail", "-c", scripts[1]],
                                               cwd=self.root, env=env, capture_output=True, text=True, timeout=15, check=False)
                    self.assertEqual(completed.returncode, 2 if profile is None else target_status,
                                     completed.stdout + completed.stderr)
                    if profile is None:
                        self.assertFalse(observed.exists())
                    else:
                        self.assertEqual([json.loads(line) for line in observed.read_text().splitlines()],
                                         [["-B", "test/ci/tools.py", "preflight", "--profile", profile], command])

    def test_functional_summaries_identify_the_selected_campaign_and_outcome(self):
        source = (local.ROOT / ".github/workflows/functional.yml").read_text()
        step = next(step for step in re.split(r"(?m)^      - ", source) if step.startswith("name: Summarize the selected boundary\n"))
        script = textwrap.dedent(re.search(r"(?ms)^        run: \|\n(.*)", step)[1])
        scopes = {
            "test-backup-restore": "Scope: logical database restore, retained state and recovered API/worker behavior.",
            "test-federation-peers": "Scope: two-Catalog indexed/live discovery, lifecycle and API/CLI/browser pagination.",
            "test-webhook-report": "Scope: standalone HTTPS receiver, durable inbox, restart and reconciliation against owned PostgreSQL.",
        }
        for suite, scope in scopes.items():
            for outcome in ("success", "failure", "skipped"):
                with self.subTest(suite=suite, outcome=outcome):
                    directory = self.base / (suite + "-" + outcome)
                    directory.mkdir()
                    summary = directory / "step-summary.md"
                    env = {**local.environment(directory), "SUITE": suite, "REPORT_DIRECTORY": str(directory / "reports"),
                           "QUALIFICATION_OUTCOME": outcome, "STATUS_AT_SUMMARY": "success" if outcome == "success" else "failure",
                           "RUNNER_OS": "Linux", "RUNNER_ARCH": "X64", "GITHUB_STEP_SUMMARY": str(summary)}
                    completed = subprocess.run(["bash", "--noprofile", "--norc", "-euo", "pipefail", "-c", script],
                                               cwd=self.root, env=env, capture_output=True, text=True, timeout=15, check=False)
                    self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
                    content = summary.read_text()
                    self.assertIn(scope, content)
                    self.assertIn("Suite: " + suite + "\n", content)
                    self.assertIn("Qualification: " + outcome + "\n", content)
                    self.assertIn("Status at summary: " + env["STATUS_AT_SUMMARY"] + "\n", content)
                    self.assertEqual(content, (directory / "reports/summary.txt").read_text())

    def test_client_dispatch_runs_one_gate_per_language_and_preserves_its_exit(self):
        source = (local.ROOT / ".github/workflows/clients.yml").read_text()
        step = next(step for step in re.split(r"(?m)^      - ", source) if "id: sdk_checks\n" in step)
        script = textwrap.dedent(re.search(r"(?ms)^        run: \|\n(.*)", step)[1])
        for language in ("go", "python", "typescript", "rust"):
            for status in (0, 7):
                with self.subTest(language=language, status=status):
                    directory = self.base / f"dispatch-{language}-{status}"
                    directory.mkdir()
                    observed = directory / "commands.jsonl"
                    executable = directory / "make"
                    executable.write_text("#!" + sys.executable + "\n" + '''import json, os, pathlib, sys
with pathlib.Path(os.environ["OBSERVED_COMMANDS"]).open("a") as output:
    output.write(json.dumps(sys.argv[1:]) + "\\n")
sys.exit(int(os.environ["CONTROL_EXIT"]))
''')
                    executable.chmod(0o755)
                    env = {**local.environment(directory), "SDK_LANGUAGE": language, "CONTROL_EXIT": str(status),
                           "OBSERVED_COMMANDS": str(observed)}
                    env["PATH"] = str(directory) + os.pathsep + env["PATH"]
                    completed = subprocess.run(["bash", "--noprofile", "--norc", "-euo", "pipefail", "-c", script],
                                               cwd=self.root, env=env, capture_output=True, text=True, timeout=15, check=False)
                    self.assertEqual(completed.returncode, status, completed.stdout + completed.stderr)
                    expected = ["webhook-report-check"] if language == "go" else ["sdk-check", "SDK_LANGUAGE=" + language]
                    self.assertEqual([json.loads(line) for line in observed.read_text().splitlines()], [expected])

    def test_client_summary_separates_receiver_sdk_and_generator_results(self):
        source = (local.ROOT / ".github/workflows/clients.yml").read_text()
        step = next(step for step in re.split(r"(?m)^      - ", source) if step.startswith("name: Record client scope\n"))
        script = textwrap.dedent(re.search(r"(?ms)^        run: \|\n(.*)", step)[1])
        cases = (("go", "success", "success", "success", "not_run"),
                 ("go", "failure", "failure", "skipped", "not_run"),
                 ("go", "failure", "success", "failure", "not_run"),
                 ("python", "success", "success", "not_run", "not_run"),
                 ("typescript", "failure", "success", "not_run", "failure"),
                 ("rust", "failure", "failure", "not_run", "not_run"))
        for index, (language, result, sdk, raw_go, editor) in enumerate(cases):
            with self.subTest(language=language, result=result, sdk=sdk):
                directory = self.base / ("client-summary-" + str(index))
                directory.mkdir()
                summary = directory / "summary.md"
                env = {**local.environment(directory), "SDK_LANGUAGE": language, "RESULT": result, "SDK_CHECKS": sdk,
                       "RAW_GO": raw_go, "EDITOR_CHECKS": editor, "SOURCE_COMMIT": self.commit,
                       "SELECTED_LANGUAGE": language, "RUNNER_TEMP": str(directory), "GITHUB_STEP_SUMMARY": str(summary)}
                completed = subprocess.run(["bash", "--noprofile", "--norc", "-euo", "pipefail", "-c", script],
                                           cwd=self.root, env=env, capture_output=True, text=True, timeout=15, check=False)
                self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
                content = summary.read_text()
                self.assertIn(f"Status at summary, before artifact upload: `{result}`.", content)
                if language == "go":
                    self.assertIn(f"Go SDK and webhook receiver gate: `{sdk}`.", content)
                    self.assertIn("Receiver-only unit coverage, when produced: `coverage/publication-report-unit.out`.", content)
                    self.assertIn("This profile excludes SDK and functional coverage.", content)
                    self.assertIn(f"Independent unmodified-generator gate: `{raw_go}`.", content)
                else:
                    self.assertIn(f"SDK gate: `{sdk}`.", content)
                    self.assertNotIn("webhook receiver gate", content)
                    self.assertNotIn("publication-report-unit.out", content)
                if language == "typescript":
                    self.assertIn(f"Editor type/lint/unit gate: `{editor}`", content)
                self.assertEqual(content, (directory / "ci-client-summary.md").read_text())

    def test_documentation_summary_reports_each_check_without_hiding_failures(self):
        source = (local.ROOT / ".github/workflows/quality.yml").read_text()
        job = re.search(r"(?ms)^  documentation:\n(.*?)(?=^  \S|\Z)", source)[1]
        step = re.search(r"(?ms)^      - name: Record documentation scope\n(.*?)(?=^      - name:|\Z)", job)[1]
        script = textwrap.dedent(re.search(r"(?ms)^        run: \|\n(.*)", step)[1])
        cases = (("success", "success", "success"), ("failure", "failure", "skipped"),
                 ("failure", "success", "failure"), ("failure", "not_run", "not_run"))
        for index, (result, markdown, site) in enumerate(cases):
            with self.subTest(result=result, markdown=markdown, site=site):
                directory = self.base / ("summary-" + str(index))
                directory.mkdir()
                summary = directory / "summary.md"
                env = {**local.environment(directory), "RESULT": result, "DOCS_SOURCE_CHECKS": markdown,
                       "DOCS_SITE_CHECKS": site, "SELECTED_SUITE": "docs", "SOURCE_COMMIT": self.commit,
                       "RUNNER_TEMP": str(directory), "GITHUB_STEP_SUMMARY": str(summary)}
                completed = subprocess.run(["bash", "--noprofile", "--norc", "-euo", "pipefail", "-c", script],
                                           env=env, cwd=self.root, capture_output=True, text=True, timeout=15, check=False)
                self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
                content = summary.read_text()
                self.assertIn(f"Markdown and repository contracts: `{markdown}`; site checks: `{site}`.", content)
                self.assertIn(f"Status at summary, before artifact upload: `{result}`.", content)
                self.assertIn("`docs-check` includes", content)
                self.assertIn("`docs-site-check` includes", content)
                self.assertEqual(content, (directory / "ci-quality-docs.md").read_text())

    def test_actual_act_lists_changed_workflows_without_jobs_or_github_credentials(self):
        selected = os.environ.get("CATALOG_TEST_ACT")
        if not selected:
            self.skipTest("explicit CATALOG_TEST_ACT selects the installed act parser control")
        with local.private_directory() as (directory, env):
            local.copy_source(self.root, directory / "source", self.commit, env)
            for name, job in (("lint", "source-local"), ("unit", "isolated-unit"), ("docs", "documentation"),
                              ("ui", "ui"), ("charts", "charts"), ("image", "images"),
                              ("security", "extended"), ("postgres", "qualification")):
                args = local.arguments(name, "backend", directory, self.commit, "123456789012345678", docker_gid=123)
                args[0] = selected
                result = local.command([*args, "--list"], env=env, cwd=directory / "invoke", timeout=30)
                self.assertIn(job, result.stdout)
                self.assertNotIn("PRIVATE-SENTINEL", result.stdout + result.stderr)
        self.assertFalse(directory.exists())

    def test_actual_act_skips_uploads_with_either_input_and_isolates_runtime_credentials(self):
        selected = os.environ.get("CATALOG_TEST_ACT")
        if not selected or os.environ.get("CATALOG_TEST_ACT_RUNNER") != "1":
            self.skipTest("explicit CATALOG_TEST_ACT_RUNNER=1 selects the network-disabled runner control")
        workflow = '''name: Local safety control
on:
  workflow_dispatch:
    inputs:
      upload_artifacts:
        type: boolean
        default: false
jobs:
  documentation:
    runs-on: ubuntu-24.04
    steps:
      - name: Use the already bound source
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          persist-credentials: false
          fetch-depth: 0
      - name: Check runtime isolation
        run: |
          test "$ACT" = true
          test "$(id -u)" != 0
          test "$GITHUB_REPOSITORY" = open-agentic-catalog/open-agentic-catalog
          test "$(git rev-parse HEAD)" = "$GITHUB_SHA"
          test "$(cat VERSION)" = 0.1.0
          test "$GOTELEMETRY" = off
          test "$ASTRO_TELEMETRY_DISABLED" = 1
          test "$NPM_CONFIG_AUDIT" = false
          test -z "${GH_TOKEN:-}"
          test -z "${ACTIONS_RUNTIME_TOKEN:-}"
          test -z "${ACTIONS_RUNTIME_URL:-}"
          test -z "${ACTIONS_RESULTS_URL:-}"
          test -z "${ACTIONS_CACHE_URL:-}"
      - name: Forbidden upload control
        if: ${{ always() && inputs.upload_artifacts && !env.ACT }}
        run: exit 93
      - name: Record completion
        run: printf 'LOCAL_SAFETY_CONTROL_PASSED\\n'
'''
        (self.root / ".github/workflows/quality.yml").write_text(workflow)
        self.git("add", ".github/workflows/quality.yml")
        self.git("commit", "--quiet", "-m", "test: add inert local act safety control")
        commit = self.git("rev-parse", "HEAD").strip()
        with local.private_directory() as (directory, env):
            local.check_runner(env, directory)
            local.copy_source(self.root, directory / "source", commit, env)
            for value in ("false", "true"):
                run_id = "12345678901234567" + ("1" if value == "true" else "0")
                args = local.arguments("docs", "backend", directory, commit, run_id)
                args[0] = selected
                args[args.index("--network") + 1] = "none"
                args[args.index("upload_artifacts=false")] = "upload_artifacts=" + value
                with local.docker_resources(env, directory, run_id):
                    result = local.command(args, env=env, cwd=directory / "invoke", timeout=90)
                self.assertIn("LOCAL_SAFETY_CONTROL_PASSED", result.stdout + result.stderr)
                self.assertNotIn("exitcode '93'", result.stdout + result.stderr)
                self.assertFalse(any((directory / "actions").iterdir()))
        self.assertFalse(directory.exists())


if __name__ == "__main__":
    unittest.main()
