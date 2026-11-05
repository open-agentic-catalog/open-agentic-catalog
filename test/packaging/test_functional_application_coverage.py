"""Exercise coverage CLI and disposable-process Make cleanup with owned stubs."""

from contextlib import redirect_stderr
from io import StringIO
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

import application_coverage as coverage
from make_test_helpers import ROOT
from test_unit_application_coverage import FILES, profile


class ProcessCoverageTests(unittest.TestCase):
    def test_all_scratch_producers_stage_and_trap_without_running_catalog_cleanup(self):
        """Qualify real recipes with bounded child fixtures, never clean targets."""
        cases = (
            ("coverage-profile", "local.", "default.out"),
            ("coverage-unit", "unit.", "unit.out"),
            ("coverage-core-profile", "core.out.", "core.out"),
            ("coverage-html", "html.", "index.html"),
            ("coverage-process-postgres-dsn-profile", "process.", "process-postgres.out"),
            ("coverage-full", "full.", "full.out"),
            ("coverage-postgres-dsn-profile", "postgres.out.", "postgres.out"),
        )
        stub_source = r"""
import json, os, pathlib, sys, time
tool = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
staging = pathlib.Path("coverage/.staging")
marker = pathlib.Path(os.environ["STAGING_MARKER"])
matches = list(staging.glob(os.environ["STAGING_PREFIX"] + "*"))
if matches and not marker.exists():
    assert len(matches) == 1, matches
    temporary = marker.with_suffix(".tmp")
    temporary.write_text(json.dumps(str(matches[0])))
    temporary.replace(marker)
    mode = os.environ["STAGING_MODE"]
    if mode == "failure":
        sys.exit(23)
    if mode in ("term", "repeated-term", "kill"):
        time.sleep(30)
profile = "mode: atomic\napp/a.go:1.1,1.2 1 1\n"
if tool == "rm":
    # Hold real cleanup at its process boundary. A second TERM must not kill
    # cleanup after the producer starts its exit trap.
    if os.environ["STAGING_MODE"] == "repeated-term":
        pathlib.Path(os.environ["CLEANUP_READY"]).write_text("ready")
        deadline = time.monotonic() + 10
        while not pathlib.Path(os.environ["CLEANUP_RELEASE"]).exists():
            if time.monotonic() >= deadline:
                sys.exit(24)
            time.sleep(0.01)
    selected = pathlib.Path(args[-1]).resolve()
    assert selected.parent == pathlib.Path("coverage/.staging").resolve(), selected
    os.execv(os.environ["NATIVE_RM"], ["rm", *args])
elif tool == "go":
    if args[0] == "list":
        print("app/a")
    elif args[0] == "test":
        for arg in args:
            if arg.startswith("-coverprofile="):
                pathlib.Path(arg.split("=", 1)[1]).write_text(profile)
    else:
        raise AssertionError(args)
elif tool == "python":
    if args[0] == "-B":
        args = args[1:]
    if args[0].endswith("unit_coverage.py"):
        print("unit fixture report")
    elif args[0].endswith("application_coverage.py"):
        if args[1] == "snapshot":
            print("sha256:fixture")
        elif args[1] == "collect":
            pathlib.Path(args[3]).write_text(profile)
        elif args[1] == "merge":
            pathlib.Path(args[4]).write_text(profile)
        elif args[1] == "full":
            pathlib.Path(args[6]).write_text(profile)
            pathlib.Path(args[7]).write_text("combined fixture report\n")
        else:
            raise AssertionError(args)
    elif not args[0].endswith("coverage_tools.py"):
        raise AssertionError(args)
elif tool == "awk":
    print(profile if matches else "app/a\n", end="")
elif tool == "gocov":
    print("{}")
elif tool == "render":
    sys.stdin.read()
    print("rendered fixture")
elif tool != "child-make":
    raise AssertionError(tool)
"""
        (ROOT / "output").mkdir(exist_ok=True)
        for target, prefix, output in cases:
            for mode in ("success", "failure", "term", "repeated-term", "kill"):
                with self.subTest(target=target, mode=mode), tempfile.TemporaryDirectory(
                    prefix="coverage-stage-", dir=ROOT / "output"
                ) as directory:
                    base = Path(directory)
                    root = base / "checkout with spaces"
                    results = root / "coverage"
                    results.mkdir(parents=True)
                    (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
                    (root / "go.mod").write_text("module app\n\ngo 1.27.0\n")
                    previous = results / output
                    previous.write_text("retained successful output")
                    report = results / "full.txt"
                    report.write_text("retained successful report")
                    if not (results / "default.out").exists():
                        (results / "default.out").write_text("mode: atomic\n")
                    tools = base / "tools"
                    tools.mkdir()
                    for name in ("go", "python", "awk", "gocov", "render", "child-make", "rm"):
                        tool = tools / name
                        tool.write_text("#!" + sys.executable + "\n" + stub_source)
                        tool.chmod(0o700)
                    marker = base / "stage.json"
                    cleanup_ready, cleanup_release = base / "cleanup-ready", base / "cleanup-release"
                    env = {key: value for key, value in os.environ.items()
                           if not key.startswith(("CATALOG_", "PYTHON", "MAKE"))
                           and key not in ("MFLAGS", "GOWORK", "GOFLAGS")}
                    env.update(PATH=str(tools) + os.pathsep + os.environ["PATH"],
                               STAGING_MARKER=str(marker), STAGING_PREFIX=prefix,
                               STAGING_MODE=mode, PYTHONDONTWRITEBYTECODE="1",
                               NATIVE_RM=shutil.which("rm"), CLEANUP_READY=str(cleanup_ready),
                               CLEANUP_RELEASE=str(cleanup_release),
                               CATALOG_TEST_DATABASE_DSN="owned-stub-never-connects")
                    arguments = ["make", "--no-print-directory", "-o", "coverage-profile"] if target in (
                        "coverage-core-profile", "coverage-html"
                    ) else ["make", "--no-print-directory"]
                    arguments += [target, "CATALOG_ENV_FILE=/dev/null", "GO=" + str(tools / "go"),
                                  "PYTHON=" + str(tools / "python"), "GOCOV=" + str(tools / "gocov"),
                                  "GOCOV_HTML=" + str(tools / "render"), "MAKE=" + str(tools / "child-make")]
                    process = subprocess.Popen(arguments, cwd=root, env=env, start_new_session=True,
                                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                    try:
                        if mode in ("term", "repeated-term", "kill"):
                            deadline = time.monotonic() + 10
                            while not marker.exists() and process.poll() is None and time.monotonic() < deadline:
                                time.sleep(0.02)
                            self.assertTrue(marker.exists(), "producer did not create scratch")
                            stage = root / json.loads(marker.read_text())
                            self.assertTrue(stage.exists())
                            os.killpg(process.pid, signal.SIGKILL if mode == "kill" else signal.SIGTERM)
                            if mode == "repeated-term":
                                deadline = time.monotonic() + 10
                                while not cleanup_ready.exists() and time.monotonic() < deadline:
                                    time.sleep(0.01)
                                self.assertTrue(cleanup_ready.exists(), "producer did not enter cleanup")
                                os.killpg(process.pid, signal.SIGTERM)
                                cleanup_release.touch()
                        stdout, stderr = process.communicate(timeout=10)
                    finally:
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                        try:
                            process.wait(timeout=10)
                        finally:
                            process.stdout.close()
                            process.stderr.close()
                    self.assertTrue(marker.exists(), stdout + stderr)
                    stage = root / json.loads(marker.read_text())
                    self.assertEqual(stage.parent, results / ".staging")
                    self.assertEqual(list(results.glob(prefix + "??????")), [])
                    self.assertEqual(process.returncode == 0, mode == "success", stdout + stderr)
                    if mode == "success":
                        self.assertNotEqual(previous.read_text(), "retained successful output")
                    else:
                        self.assertEqual(previous.read_text(), "retained successful output")
                        self.assertEqual(report.read_text(), "retained successful report")
                    if mode == "kill":
                        self.assertEqual(list((results / ".staging").iterdir()), [stage])
                        self.assertTrue(stage.exists())
                    else:
                        self.assertEqual(list((results / ".staging").iterdir()), [])

    def test_existing_make_gates_reject_rounded_false_passes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
            (root / "go.mod").write_text("module app\ngo 1.27.0\n")
            for name in ("application_coverage.py", "unit_coverage.py"):
                destination = root / "test/packaging" / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes((ROOT / "test/packaging" / name).read_bytes())
            (root / "coverage").mkdir()
            profile_text = "mode: atomic\napp/a/main.go:1.1,2.1 30350 1\napp/a/main.go:3.1,4.1 2041 0\n"
            for name in ("default", "core", "postgres"):
                (root / "coverage" / (name + ".out")).write_text(profile_text)
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith("CATALOG_") and key not in ("MAKEFLAGS", "MFLAGS")}
            for target, variable in (("coverage-check", "COVERAGE_MIN"), ("coverage-core-check", "CORE_COVERAGE_MIN"),
                                     ("coverage-postgres-dsn-check", "POSTGRES_COVERAGE_MIN"),
                                     ("coverage-core-check", "CRITICAL_PACKAGE_COVERAGE_MIN")):
                for minimum in ("93.69", "93.7", "nan"):
                    with self.subTest(target=target, variable=variable, minimum=minimum):
                        result = subprocess.run(
                            ["make", "--no-print-directory", target, "-o", "coverage-profile", "-o", "coverage-core-profile",
                             "-o", "coverage-postgres-dsn-profile", "CATALOG_ENV_FILE=/dev/null", "CORE_COVERAGE_MIN=0",
                             "CRITICAL_COVERAGE_PACKAGES=app/a", "CRITICAL_PACKAGE_COVERAGE_MIN=0", variable + "=" + minimum],
                            cwd=root, env=environment, capture_output=True, text=True, timeout=10, check=False)
                        self.assertEqual(result.returncode == 0, minimum == "93.69", result.stdout + result.stderr)
                        if minimum == "93.7":
                            self.assertIn("30350/32391", result.stderr)
                            self.assertIn("below the 93.7% minimum", result.stderr)
                        elif minimum == "nan":
                            self.assertIn(variable + " must be a number", result.stderr)

    def test_full_target_publishes_only_after_fresh_successful_stable_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", str(root)], check=True, timeout=10)
            (root / ".gitignore").write_text("coverage/\ncalls\n")
            (root / "source").write_text("original")
            (root / "go.mod").write_text("module app\ngo 1.27.0\n")
            (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
            variables = ("UNIT_COVERAGE_MIN", "UNIT_CRITICAL_PACKAGE_COVERAGE_MIN", "COVERAGE_MIN", "CORE_COVERAGE_MIN",
                         "CRITICAL_PACKAGE_COVERAGE_MIN", "POSTGRES_COVERAGE_MIN", "FULL_COVERAGE_MIN", "CRITICAL_COVERAGE_PACKAGES")
            (root / "thresholds.mk").write_text("include Makefile\nprint-values:\n\t@printf '%s\\n' " +
                                               " ".join("'$(" + variable + ")'" for variable in variables) + "\n")
            for name in ("test/packaging/application_coverage.py", "test/packaging/unit_coverage.py", "test/functional/runner.py"):
                destination = root / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes((ROOT / name).read_bytes())
            results = root / "coverage"
            results.mkdir()
            child = root / "make-child"
            child.write_text("#!" + sys.executable + "\n" + '''
import json, os, pathlib, subprocess, sys
values = subprocess.run(['make', '--no-print-directory', '-s', '-f', 'thresholds.mk', 'print-values'],
                        check=True, capture_output=True, text=True, timeout=10).stdout.splitlines()
with pathlib.Path('calls').open('a') as output:
    output.write(json.dumps({"args": sys.argv[1:], "values": values}) + '\\n')
if os.environ['CASE'] == 'failed':
    raise SystemExit(7)
if 'coverage-unit' in sys.argv:
    pathlib.Path('coverage/unit.out').write_text(UNIT)
else:
    pathlib.Path('coverage/default.out').write_text(LOCAL)
    pathlib.Path('coverage/postgres.out').write_text(POSTGRES)
    pathlib.Path('coverage/process-postgres.out').write_text(PROCESS)
    if os.environ['CASE'] == 'changed':
        pathlib.Path('source').write_text('changed')
'''.replace("UNIT", repr(coverage.encode(profile())))
                .replace("LOCAL", repr(coverage.encode(profile(hit=True))))
                .replace("POSTGRES", repr(coverage.encode(profile(FILES[-1:], True))))
                .replace("PROCESS", repr(coverage.encode(profile(FILES[:3], True)))))
            child.chmod(0o700)
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith("CATALOG_") and key not in ("MAKEFLAGS", "MFLAGS") and key not in variables}
            for case in ("failed", "changed", "floor", "default_floor", "success", "overrides"):
                (root / "source").write_text("original")
                (root / "calls").write_text("")
                (results / "full.out").write_text("retained profile")
                (results / "full.txt").write_text("retained report")
                overrides = [] if case == "default_floor" else ["FULL_COVERAGE_MIN=" + ("80.001" if case == "floor" else "80")]
                expected = ["90", "90", "90", "93", "90", "95", "95" if case == "default_floor" else overrides[0].split("=")[1]]
                if case == "overrides":
                    expected[:6] = ["91", "92", "91", "94", "91", "96"]
                    overrides.extend(variable + "=" + value for variable, value in zip(variables[:6], expected[:6], strict=True))
                    overrides.append("CRITICAL_COVERAGE_PACKAGES=app/a app/b")
                result = subprocess.run(["make", "--no-print-directory", "coverage-full", "MAKE=" + str(child),
                                         "CATALOG_ENV_FILE=/missing/ignored.env", *overrides], cwd=root,
                                        env=environment | {"CASE": case}, capture_output=True, text=True, timeout=30, check=False)
                self.assertEqual(result.returncode == 0, case in ("success", "overrides"), result.stderr)
                calls = [json.loads(line) for line in (root / "calls").read_text().splitlines()]
                self.assertIn("coverage-unit", calls[0]["args"])
                for call in calls:
                    self.assertEqual(call["values"][:7], expected)
                    selected = call["values"][7].split()
                    if case == "overrides":
                        self.assertEqual(selected, ["app/a", "app/b"])
                    else:
                        self.assertEqual(len(selected), 20)
                        self.assertIn("app/internal/platform/authn", selected)
                        self.assertIn("app/internal/draftfile", selected)
                if case == "failed":
                    self.assertEqual(len(calls), 1)
                else:
                    self.assertEqual(len(calls), 2)
                    self.assertIn("coverage-process-postgres-profile", calls[1]["args"])
                if case in ("success", "overrides"):
                    self.assertIn("Combined application: 40/50", (results / "full.txt").read_text())
                    self.assertIn("Source digest: sha256:", (results / "full.txt").read_text())
                else:
                    self.assertEqual((results / "full.out").read_text(), "retained profile")
                    self.assertEqual((results / "full.txt").read_text(), "retained report")
                if case == "floor":
                    self.assertIn("below the 80.001% minimum", result.stderr)
                if case == "default_floor":
                    self.assertIn("below the 95% minimum", result.stderr)
                if case == "changed":
                    self.assertIn("Source changed during full coverage", result.stderr)
                self.assertTrue((results / ".staging").is_dir())
                self.assertEqual(list((results / ".staging").iterdir()), [])

    def test_collection_requires_metadata_counters_and_valid_tool_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            child = root / "child"
            child.mkdir()
            output = root / "profile"
            go = root / "go fixture"
            go.write_text("#!" + sys.executable + "\n" +
                          "import pathlib, sys\n"
                          "assert sys.argv[1:4] == ['tool', 'covdata', 'textfmt']\n"
                          "pathlib.Path(sys.argv[-1][3:]).write_text('mode: atomic\\napp/main.go:1.1,2.1 1 1\\n')\n")
            go.chmod(0o700)
            arguments = ["collect", str(root), str(output), "--go", str(go)]
            with redirect_stderr(StringIO()):
                self.assertEqual(coverage.main(arguments), 1)
                (child / "covmeta.fixture").touch()
                self.assertEqual(coverage.main(arguments), 1)
            self.assertFalse(output.exists())
            (child / "covcounters.fixture").touch()
            self.assertEqual(coverage.main(arguments), 0)
            self.assertIn("app/main.go", output.read_text())
            go.write_text("#!" + sys.executable + "\nraise SystemExit(9)\n")
            with redirect_stderr(StringIO()):
                self.assertEqual(coverage.main(arguments), 1)

    def test_owned_compose_cleanup_runs_after_success_and_test_failure_and_propagates_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
            (root / "go.mod").write_text("module app\ngo 1.27.0\n")
            compose, go = root / "compose", root / "go"
            log = root / "calls"
            compose.write_text("#!" + sys.executable + "\n" + '''
import json, os, pathlib, sys
args = sys.argv[1:]
with pathlib.Path(os.environ['CALLS']).open('a') as output:
    output.write(json.dumps(args) + '\\n')
if 'port' in args:
    print('127.0.0.1:54399')
if 'down' in args:
    raise SystemExit(int(os.environ['CLEANUP_EXIT']))
''')
            go.write_text("#!" + sys.executable + "\nimport os\nraise SystemExit(int(os.environ['TEST_EXIT']))\n")
            for executable in (compose, go):
                executable.chmod(0o700)
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith("CATALOG_") and key not in ("MAKEFLAGS", "MFLAGS")}
            for test_exit, cleanup_exit in ((0, 0), (7, 0), (0, 9)):
                log.write_text("")
                result = subprocess.run(["make", "--no-print-directory", "test-process-postgres", "CATALOG_ENV_FILE=/dev/null",
                                         "COMPOSE=" + str(compose), "GO=" + str(go)], cwd=root,
                                        env=environment | {"CALLS": str(log), "TEST_EXIT": str(test_exit), "CLEANUP_EXIT": str(cleanup_exit)},
                                        capture_output=True, text=True, timeout=20, check=False)
                self.assertEqual(result.returncode == 0, test_exit == cleanup_exit == 0, result.stderr)
                calls = [json.loads(line) for line in log.read_text().splitlines()]
                self.assertIn("up", calls[0])
                self.assertIn("down", calls[-1])
                self.assertIn("--volumes", calls[-1])
                self.assertEqual(len({call[call.index('-p') + 1] for call in calls}), 1)
