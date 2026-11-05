"""Real output-cleanup commands in owned Git fixtures; never the working checkout."""

import json
import os
import stat
import subprocess
import sys
import unittest

import clean
import test_functional_clean as fixtures


class OutputCleanupTests(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.CleanTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        renamed = self.fixture.root.parent / "checkout space\\name"
        self.fixture.root.rename(renamed)
        self.fixture.root = renamed
        self.root = self.fixture.root
        self.write = self.fixture.write
        fixtures.install_make(self.root)

    def invoke(self, *targets):
        return subprocess.run(["make", "--no-print-directory", *targets], cwd=self.root,
                              capture_output=True, text=True, timeout=30, check=False)

    def test_known_output_removal_and_preserved_input_classes(self):
        directories = ("review", "sdk", "mutation", "ui-mutation", "property", "playwright",
                       "coverage", "verification", "ui-dependencies", "artifacts", "vscode/staging",
                       "n88/go-build-cache", "n88/trivy-cache", "deadcode-research/n16-rebuild/go-cache")
        for name in directories:
            self.write("output/" + name + "/result", "disposable result")
        self.write("output/session.log", "disposable log")
        protected = ("demo/receipt.json", "ui-compose/development/inputs/key", "monitoring/password",
                     "sonarqube/credentials.json", "backup/dump", "backups/dump", "native-import/receipt",
                     "interoperability/installation.json", "langgraph-model/weights",
                     "search-relevance/models/weights", "performance/state", "unknown/private", "settings.json")
        retained = [self.write("output/" + name, "PRIVATE-DO-NOT-PRINT") for name in protected]
        retained += [self.write(name) for name in (".env", "node_modules/dependency",
                                                  "bin/catalogd", "test/__pycache__/example.pyc")]
        for path in retained:
            path.chmod(0o600)
        readonly = self.root / "output/sdk"
        readonly.chmod(0o555)
        before = fixtures.snapshot(self.root)
        index = (self.root / ".git/index").read_bytes()
        preview = self.invoke("clean-output-dry-run")
        self.assertEqual(preview.returncode, 0, preview.stderr)
        for name in directories:
            self.assertIn("Would remove output/" + name + "\n", preview.stdout)
        self.assertIn("Would remove output/session.log", preview.stdout)
        self.assertIn("Preserving output/demo:", preview.stdout)
        self.assertIn("Preserving output/settings.json: outside output cleanup inventory", preview.stdout)
        self.assertNotIn("PRIVATE-DO-NOT-PRINT", preview.stdout + preview.stderr)
        self.assertEqual(fixtures.snapshot(self.root), before)
        self.assertEqual((self.root / ".git/index").read_bytes(), index)
        result = self.invoke("clean-output")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(all(not (self.root / "output" / name).exists() for name in directories))
        self.assertFalse((self.root / "output/session.log").exists())
        after = fixtures.snapshot(self.root)
        for path in retained:
            name = str(path.relative_to(self.root))
            self.assertEqual(after[name], before[name])
        self.assertEqual((self.root / ".git/index").read_bytes(), index)
        repeated = self.invoke("clean-output")
        self.assertEqual(repeated.returncode, 0, repeated.stderr)
        self.assertIn("No known output artifacts", repeated.stdout)
        self.assertEqual(fixtures.snapshot(self.root), after)

    def test_nested_git_repositories_and_sibling_links(self):
        repository = self.root / "output/review/group/repository"
        repository.mkdir(parents=True)
        clean.git(repository, "init", "--quiet")
        retained = self.write("output/review/group/repository/uncommitted.txt", "private work")
        marker = self.write("output/review/group/worktree/.git", "gitdir: somewhere")
        self.write("output/review/group/worktree/private.txt")
        self.write("output/review/group/report.json")
        self.write("output/review/disposable/result")
        outside = self.write("outside/sentinel", "outside data").parent
        (self.root / "output/review/link").symlink_to(outside, target_is_directory=True)
        repository.chmod(0o555)
        before = fixtures.snapshot(self.root)
        preview = self.invoke("clean-output-dry-run")
        self.assertEqual(preview.returncode, 0, preview.stderr)
        self.assertIn("Preserving output/review/group/repository: nested Git repository", preview.stdout)
        self.assertIn("Preserving output/review/group/worktree: nested Git repository", preview.stdout)
        self.assertEqual(fixtures.snapshot(self.root), before)
        result = self.invoke("clean-output")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(retained.is_file())
        self.assertTrue(marker.is_file())
        self.assertFalse((self.root / "output/review/group/report.json").exists())
        self.assertFalse((self.root / "output/review/disposable").exists())
        self.assertFalse((self.root / "output/review/link").is_symlink())
        after = fixtures.snapshot(self.root)
        for name,value in before.items():
            if name.startswith(("output/review/group/repository", "output/review/group/worktree", "outside")):
                self.assertEqual(after[name], value)
        self.assertEqual(stat.S_IMODE(repository.stat().st_mode), 0o555)
        self.assertEqual((outside / "sentinel").read_text(), "outside data")
        again = self.invoke("clean-output")
        self.assertEqual(again.returncode, 0, again.stderr)
        self.assertEqual(fixtures.snapshot(self.root), after)

    def test_preflight_refuses_tracked_content_before_any_removal(self):
        self.write("output/a.log")
        self.write("output/review/tracked.txt")
        clean.git(self.root, "add", "--force", "--", "output/review/tracked.txt")
        before = fixtures.snapshot(self.root)
        for goal in ("clean-output-dry-run", "clean-output"):
            result = self.invoke(goal)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Refusing tracked content", result.stderr)
            self.assertNotIn("Removing ", result.stdout)
            self.assertEqual(fixtures.snapshot(self.root), before)

    def test_selected_symlinks_and_invalid_types_refuse_before_removal(self):
        self.write("output/a.log")
        outside = self.write("outside/keep").parent
        invalid = self.root / "output/review"
        invalid.symlink_to(outside, target_is_directory=True)
        for kind in ("symlink", "file"):
            if kind == "file":
                invalid.unlink()
                invalid.write_text("not a report directory")
            before = fixtures.snapshot(self.root)
            for goal in ("clean-output-dry-run", "clean-output"):
                result = self.invoke(goal)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Refusing", result.stderr)
                self.assertEqual(fixtures.snapshot(self.root), before)

    def test_unreadable_subtree_prevents_removal_and_permission_changes(self):
        if os.geteuid() == 0:
            self.skipTest("Unreadable subtree control requires an ordinary UID")
        earlier = self.write("output/a.log")
        readonly = self.write("output/artifacts/cache/file").parent
        readonly.chmod(0o555)
        inaccessible = self.write("output/review/sealed/file").parent
        before = fixtures.snapshot(self.root)
        inaccessible.chmod(0)
        try:
            for goal in ("clean-output-dry-run", "clean-output"):
                result = self.invoke(goal)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Permission denied", result.stderr)
                self.assertTrue(earlier.exists())
                self.assertEqual(stat.S_IMODE(readonly.stat().st_mode), 0o555)
                self.assertEqual(stat.S_IMODE(inaccessible.stat().st_mode), 0)
        finally:
            inaccessible.chmod(0o755)
        self.assertEqual(fixtures.snapshot(self.root), before)

    def test_all_cleanup_goals_are_isolated_before_env_inclusion(self):
        self.write(".env", "$(error Cleanup loaded private configuration)\n")
        self.write("output/review/result")
        before = fixtures.snapshot(self.root)
        for goal in ("clean", "clean-dry-run", "clean-output", "clean-output-dry-run"):
            for other in ("build", "clean", "clean-output"):
                result = self.invoke("-j2", goal, other)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Cleanup targets must run alone", result.stderr)
                self.assertNotIn("Cleanup loaded", result.stderr)
                self.assertEqual(fixtures.snapshot(self.root), before)
        for goal in ("clean-output-dry-run", "clean-output"):
            result = self.invoke(goal)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn("Cleanup loaded", result.stderr)
        self.assertTrue((self.root / ".env").exists())

    def test_missing_output_and_unknown_only_output_are_noops(self):
        before = fixtures.snapshot(self.root)
        for goal in ("clean-output-dry-run", "clean-output"):
            result = self.invoke(goal)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("No known output artifacts", result.stdout)
            self.assertEqual(fixtures.snapshot(self.root), before)
        self.write("output/private-token.json")
        before = fixtures.snapshot(self.root)
        result = self.invoke("clean-output")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Preserving output/private-token.json:", result.stdout)
        self.assertEqual(fixtures.snapshot(self.root), before)

    def test_console_escapes_literal_log_names(self):
        name = "output/- [report]\n\x1b[31m.log"
        self.write(name)
        before = fixtures.snapshot(self.root)
        preview = self.invoke("clean-output-dry-run")
        self.assertEqual(preview.returncode, 0, preview.stderr)
        self.assertIn("Would remove " + json.dumps(name), preview.stdout)
        self.assertNotIn("\x1b", preview.stdout)
        self.assertEqual(fixtures.snapshot(self.root), before)
        result = self.invoke("clean-output")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.root / name).exists())

    def test_checkout_lock_prevents_all_root_cleanup_goals(self):
        self.write("output/review/result")
        self.write("bin/catalogd")
        before = fixtures.snapshot(self.root)
        with clean.checkout_guard(self.root):
            for goal in ("clean-dry-run", "clean", "clean-output-dry-run", "clean-output"):
                result = self.invoke(goal)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Another VS Code", result.stderr)
                self.assertEqual(fixtures.snapshot(self.root), before)

    def test_all_scopes_handle_trailing_whitespace_in_checkout_paths(self):
        for name in ("checkout trailing space ", "checkout trailing newline\n"):
            with self.subTest(path=name):
                renamed = self.root.parent / name
                self.root.rename(renamed)
                self.root = renamed
                self.fixture.root = renamed
                self.write("output/review/result")
                self.write("bin/catalogd")
                for preview, removal, selected in (
                        ("clean-output-dry-run", "clean-output", "output/review"),
                        ("clean-dry-run", "clean", "bin/catalogd")):
                    before = fixtures.snapshot(self.root)
                    result = self.invoke(preview)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(fixtures.snapshot(self.root), before)
                    result = self.invoke(removal)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertFalse((self.root / selected).exists())

    def test_real_output_bind_mount_refusals(self):
        import shutil
        if sys.platform != "linux" or any(shutil.which(tool) is None for tool in ("unshare", "mount", "umount")):
            self.skipTest("Real bind qualification needs Linux mount tools")
        namespace = ["unshare", "--user", "--map-current-user", "--mount", "--keep-caps", "--propagation", "private"]
        available = subprocess.run([*namespace, "true"], capture_output=True, timeout=10, check=False)
        if available.returncode:
            self.skipTest("Owned user/mount namespace is unavailable")
        result = subprocess.run([*namespace, sys.executable, "-B", fixtures.__file__, "--qualify-output-mounts",
                                 self.fixture.temporary.name], capture_output=True, text=True, timeout=180, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        facts = json.loads(result.stdout)
        self.assertEqual(facts["sameFilesystemBindCases"], 5)
        self.assertEqual(facts["publicTargetRefusals"], 10)
        self.assertTrue(facts["contentsAndModesPreserved"])
        self.assertTrue(facts["unmounted"])
        print("Real output mount qualification: " + result.stdout.strip())


if __name__ == "__main__":
    unittest.main()
