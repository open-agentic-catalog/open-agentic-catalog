"""Qualify actual Git, pinned bump tooling and locked installs in disposable trees."""

from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import tomllib
import unittest
from unittest.mock import patch

import versioning as v

BMV = v.ROOT / ".tools/dev/bin/bump-my-version"


def make_environment():
    """Do not inherit the outer gate's Make recursion, dry-run or overrides."""
    return {key:value for key,value in v.environment().items()
            if key not in ("MAKEFLAGS","MFLAGS","MAKELEVEL","MAKEOVERRIDES")}


class GitVersionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog version fixture ")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for name in ("VERSION", "pyproject.toml", "uv.lock", "ruff.toml", "go.mod"):
            shutil.copyfile(v.ROOT / name, self.root / name)
        for name in {rule["filename"] for rule in v.bump_files()} - {"VERSION"}:
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(v.ROOT / name, target)
        (self.root / ".gitignore").write_text(".tools/\nignored/\n")
        self.git("init", "-q")
        self.git("config", "user.name", "Local fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")
        self.head = self.git("rev-parse", "HEAD")

    def git(self, *args):
        return v.run(["git", *args], self.root)

    def test_real_git_source_states_tagging_and_override_isolation(self):
        self.assertEqual(v.metadata(self.root)["sourceState"], "clean")
        self.assertIn("-dev+g", v.metadata(self.root)["version"])
        self.git("tag", "v0.1.0")
        self.assertEqual(v.release(self.root)["version"], "0.1.0")
        with patch.dict(os.environ, {"GIT_DIR": str(v.ROOT / ".git")}):
            self.assertEqual(v.metadata(self.root)["commit"], self.head)
        (self.root / "ignored").mkdir()
        (self.root / "ignored/report").write_text("ignored output")
        self.assertEqual(v.metadata(self.root)["sourceState"], "clean")
        (self.root / "new.txt").write_text("new")
        self.assertEqual(v.metadata(self.root)["sourceState"], "modified")
        self.assertIn("-dev+g", v.metadata(self.root)["version"])
        self.git("add", "new.txt")
        self.assertEqual(v.metadata(self.root)["sourceState"], "modified")
        self.git("commit", "-qm", "new file")
        (self.root / "new.txt").write_text("unstaged")
        self.assertEqual(v.metadata(self.root)["sourceState"], "modified")
        self.assertIn("-dev+g", v.metadata(self.root)["version"])
        with self.assertRaises(ValueError):
            v.release(self.root)

    def test_shallow_clone_has_exact_head_identity_and_local_tag(self):
        self.git("tag", "v0.1.0")
        clone = self.root / "ignored/shallow"
        clone.parent.mkdir()
        self.git("clone", "--depth", "1", "file://" + str(self.root), str(clone))
        self.assertEqual(v.release(clone), {"version": "0.1.0", "commit": self.head, "sourceState": "clean"})

    def test_gitless_nested_archive_and_failed_git_are_distinct(self):
        archive = self.root / "ignored/archive"
        archive.mkdir(parents=True)
        shutil.copyfile(self.root / "VERSION", archive / "VERSION")
        self.assertEqual(v.metadata(archive), {"version":"0.1.0-dev", "commit":"unknown", "sourceState":"unknown"})
        (archive / ".git").mkdir()
        with self.assertRaises(ValueError):
            v.metadata(archive)

    def test_make_override_validation_precedes_shell_and_make_expansion(self):
        shutil.copyfile(v.ROOT / "Makefile", self.root / "Makefile")
        helper = self.root / "test/packaging"
        helper.mkdir(parents=True)
        shutil.copyfile(v.ROOT / "test/packaging/versioning.py", helper / "versioning.py")
        marker = self.root / "must-not-exist"
        for payload in ("x'; touch " + str(marker) + "; '", "x`touch " + str(marker) + "`", "x$(shell touch " + str(marker) + ")", "x\nother"):
            result = subprocess.run(["make", "--no-print-directory", "version", "VERSION=" + payload], cwd=self.root,
                                    env=make_environment(), capture_output=True, text=True, timeout=30, check=False)
            self.assertNotEqual(result.returncode,0)
            self.assertFalse(marker.exists(), "untrusted metadata was executed")
        result = subprocess.run(["make","--no-print-directory","version","VERSION=functional","COMMIT=fixture"],cwd=self.root,
                                env=make_environment(),capture_output=True,text=True,timeout=30,check=True)
        self.assertEqual(__import__("json").loads(result.stdout), {"version":"functional","commit":"fixture","sourceState":"unknown"})

    def test_make_interpreter_default_owned_path_spaces_and_explicit_override(self):
        shutil.copyfile(v.ROOT / "Makefile", self.root / "Makefile")
        probe = self.root / "probe.mk"
        probe.write_text('include Makefile\nprobe:\n\t@cd test && $(PYTHON) -c "import sys; print(sys.executable)"\n')
        (self.root / "test").mkdir(exist_ok=True)
        def execute(*arguments):
            return subprocess.run(["make", "--no-print-directory", "-f", "probe.mk", "probe", *arguments], cwd=self.root,
                                  env=make_environment(), capture_output=True, text=True, timeout=30, check=True).stdout.strip()
        system = execute("PYTHON=python3")
        self.assertEqual(execute(), system)
        owned = self.root / ".tools/dev/bin"
        owned.mkdir(parents=True)
        script = owned / "python"
        script.write_text('#!/bin/sh\nprintf "owned interpreter\\n"\n')
        script.chmod(0o700)
        self.assertEqual(execute(), "owned interpreter")
        self.assertEqual(execute("PYTHON=python3"), system)
        # The shell restore harness consumes one executable path, not shell text.
        # Exercise its actual Make handoff with a selected path containing spaces.
        script.unlink()
        script.symlink_to(system)
        handoff = (v.ROOT / "Makefile").read_text().split('CATALOG_FUNCTIONAL_PYTHON_COMMAND=', 1)[1].split(' sh test/functional/restore.sh', 1)[0]
        with probe.open("a") as target:
            target.write('\nhandoff:\n\t@CATALOG_FUNCTIONAL_PYTHON_COMMAND=' + handoff + ' sh ./handoff.sh\n')
        (self.root / "handoff.sh").write_text('"$CATALOG_FUNCTIONAL_PYTHON_COMMAND" -I -c "import sys; print(sys.executable)"\n')
        reply = subprocess.run(["make", "--no-print-directory", "-f", "probe.mk", "handoff"], cwd=self.root,
                               env=make_environment(), capture_output=True, text=True, timeout=30, check=True)
        self.assertEqual(reply.stdout.strip(), str(script))

    @unittest.skipUnless(BMV.is_file(), "make dev-tools-install required for pinned bump qualification")
    def test_actual_bump_dry_run_numeric_rc_final_and_no_repository_mutation(self):
        expected = (("minor", "0.2.0"), ("rc", "0.2.0-rc.1"), ("rc", "0.2.0-rc.2"), ("final", "0.2.0"),
                    ("patch", "0.2.1"), ("rc", "0.2.1-rc.1"), ("minor", "0.3.0"), ("rc", "0.3.0-rc.1"),
                    ("patch", "0.3.1"), ("rc", "0.3.1-rc.1"), ("major", "1.0.0"))
        original_config = (self.root / "pyproject.toml").read_bytes()
        original_openapi = (self.root / "api/openapi/control-v1.yaml").read_text()
        def dependencies():
            python = tomllib.loads((self.root / "test/sdk/python/uv.lock").read_text())
            example = tomllib.loads((self.root / "examples/langgraph/uv.lock").read_text())
            npm = json.loads((self.root / "sdk/typescript/package-lock.json").read_text())
            return ([p for p in python["package"] if p["name"] != "open-agentic-catalog"],
                    [p for p in example["package"] if p["name"] != "open-agentic-catalog"],
                    [p for p in tomllib.loads((self.root / "examples/rust-catalog-report/Cargo.lock").read_text())["package"] if p["name"] != "open-agentic-catalog"],
                    {name: value for name, value in npm["packages"].items() if name})
        original_dependencies = dependencies()
        for part, new in expected:
            # Git must detect each same-size bump despite coarse cached timestamps.
            for name in {rule["filename"] for rule in v.bump_files()}:
                os.utime(self.root / name, (946684800, 946684800))
            self.git("update-index", "--refresh")
            previous = v.base_version(self.root)
            before = self.git("rev-parse", "HEAD")
            with redirect_stdout(io.StringIO()), patch.dict(os.environ, {"BUMPVERSION_COMMIT": "true", "BUMPVERSION_TAG": "true", "GIT_DIR": str(v.ROOT / ".git")}):
                v.bump(self.root, str(BMV), part, True)
                self.assertEqual(v.base_version(self.root), previous)
                v.bump(self.root, str(BMV), part, False)
            self.assertEqual(v.base_version(self.root), new)
            self.assertEqual((self.root / "api/openapi/control-v1.yaml").read_text(),
                             original_openapi.replace("\n  version: 0.1.0\n", "\n  version: " + new + "\n", 1))
            self.assertEqual((self.root / "pyproject.toml").read_bytes(), original_config)
            self.assertEqual(self.git("rev-parse", "HEAD"), before)
            self.assertEqual(self.git("tag"), "")
            self.assertEqual(set(self.git("diff", "--name-only").splitlines()), {rule["filename"] for rule in v.bump_files()}, (part, previous, new))
            v.check(self.root)
            self.assertEqual(dependencies(), original_dependencies)
            with self.assertRaises(ValueError):
                v.bump(self.root, str(BMV), "patch", False)
            self.git("add", *sorted({rule["filename"] for rule in v.bump_files()}))
            self.git("commit", "-qm", "fixture version")

    def test_make_version_check_refuses_stale_openapi_without_modifying_files(self):
        shutil.copyfile(v.ROOT / "Makefile", self.root / "Makefile")
        helper = self.root / "test/packaging"
        helper.mkdir(parents=True)
        shutil.copyfile(v.ROOT / "test/packaging/versioning.py", helper / "versioning.py")
        path = self.root / "api/openapi/control-v1.yaml"
        stale = path.read_text().replace("\n  version: 0.1.0\n", "\n  version: 1.0.0\n", 1)
        path.write_text(stale)
        result = subprocess.run(["make", "--no-print-directory", "version-check"], cwd=self.root,
                                env=make_environment(), capture_output=True, text=True, timeout=30, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("OpenAPI info.version must match VERSION", result.stderr)
        self.assertEqual(path.read_text(), stale)
        self.assertEqual(v.base_version(self.root), "0.1.0")

    @unittest.skipUnless(BMV.is_file(), "make dev-tools-install required for actual Make bump qualification")
    def test_make_bump_runs_alone_ignores_workflow_file_and_updates_release_metadata(self):
        shutil.copyfile(v.ROOT / "Makefile", self.root / "Makefile")
        helper = self.root / "test/packaging"
        helper.mkdir(parents=True)
        shutil.copyfile(v.ROOT / "test/packaging/versioning.py", helper / "versioning.py")
        with (self.root / ".gitignore").open("a") as target:
            target.write(".env\n")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture make tooling")
        before = self.git("rev-parse","HEAD")
        (self.root / ".env").write_text("$(error workflow file must not load)\n")
        command = ["make", "--no-print-directory", "version-bump-dry-run", "PART=rc", "BUMPVERSION=" + str(BMV)]
        subprocess.run(command, cwd=self.root,env=make_environment(),capture_output=True,text=True,timeout=30,check=True)
        self.assertEqual(v.base_version(self.root),"0.1.0")
        combined = subprocess.run(command + ["build"],cwd=self.root,env=make_environment(),capture_output=True,text=True,timeout=30,check=False)
        self.assertNotEqual(combined.returncode,0)
        command[2] = "version-bump"
        subprocess.run(command, cwd=self.root,env=make_environment(),capture_output=True,text=True,timeout=30,check=True)
        self.assertEqual(v.base_version(self.root),"0.1.0-rc.1")
        self.assertEqual(set(self.git("diff","--name-only").splitlines()), {rule["filename"] for rule in v.bump_files()})
        v.check(self.root)
        self.assertEqual(self.git("rev-parse","HEAD"),before)
        self.assertEqual(self.git("tag"),"")

    @unittest.skipUnless(os.environ.get("CATALOG_TEST_UV"), "select pinned CATALOG_TEST_UV for installer qualification")
    def test_locked_install_repeat_stale_lock_and_environment_isolation(self):
        uv = os.environ["CATALOG_TEST_UV"]
        before = (self.root / "uv.lock").read_bytes()
        personal = self.root / "ignored/personal"
        personal.mkdir(parents=True)
        sentinel = personal / "keep"
        sentinel.write_text("keep")
        user_config = self.root / "ignored/config"
        (user_config / "uv").mkdir(parents=True)
        (user_config / "uv/uv.toml").write_text('required-version = "==0.0.0"\n')
        with patch.dict(os.environ, {"UV_PROJECT_ENVIRONMENT": str(personal), "VIRTUAL_ENV": str(personal), "XDG_CONFIG_HOME":str(user_config)}), redirect_stdout(io.StringIO()):
            v.install(self.root, uv, shutil.which("python3"))
            v.install(self.root, uv, shutil.which("python3"))
        self.assertEqual(sentinel.read_text(), "keep")
        self.assertFalse((self.root / ".venv").exists())
        self.assertEqual((self.root / "uv.lock").read_bytes(), before)
        manifest = self.root / "pyproject.toml"
        manifest.write_text(manifest.read_text().replace("ruff==0.17.3", "ruff==0.16.6"))
        with patch.object(v, "check"), self.assertRaises(ValueError):
            v.install(self.root, uv, shutil.which("python3"))
        self.assertEqual((self.root / "uv.lock").read_bytes(), before)
