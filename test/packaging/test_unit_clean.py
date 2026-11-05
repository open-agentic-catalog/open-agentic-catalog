"""Cleanup orchestration and read-only selection units; no Git, subprocesses or deletion."""

from contextlib import contextmanager, redirect_stdout
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import clean


class PreflightTests(unittest.TestCase):
    def test_one_complete_preflight_runs_under_the_checkout_lock(self):
        root = Path("/catalog-clean-unit-fixture")
        for scope in ("all", "output", "vscode"):
            for dry_run in (False, True):
                with self.subTest(scope=scope, dry_run=dry_run):
                    events = []

                    @contextmanager
                    def guard(actual):
                        self.assertEqual(actual, root)
                        events.append("locked")
                        try:
                            yield
                        finally:
                            events.append("released")

                    def select(actual, *, scope):
                        self.assertEqual(actual, root)
                        self.assertEqual(events, ["locked"])
                        return [], {}

                    with patch.object(Path, "resolve", return_value=root), \
                            patch.object(clean, "checkout_guard", side_effect=guard), \
                            patch.object(clean, "plan", side_effect=select) as planner, \
                            patch.object(clean, "git", side_effect=AssertionError("unit test called Git")), \
                            redirect_stdout(io.StringIO()):
                        self.assertEqual(clean.clean(root, dry_run=dry_run, scope=scope), [])
                    planner.assert_called_once_with(root, scope=scope)
                    self.assertEqual(events, ["locked", "released"])


class EmptyParentTests(unittest.TestCase):
    """Keep parent selection read-only and preserve unrelated child entries."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog-empty-parent-unit-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def write(self, name):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("preserved until actual cleanup")
        return path

    def test_empty_and_planned_empty_parents_are_selected_in_child_first_order(self):
        files = [self.write(name) for name in (
            ".tools/dev/bin/python", "bin/catalogd", "bin/examples/publication-report",
            "documentation/public/generated/manual.json")]
        removals = [Path(name) for name in (
            ".tools/dev", "bin/catalogd", "bin/examples/publication-report", "documentation/public/generated")]
        expected = [Path(".tools"), Path("bin/examples"), Path("bin"), Path("documentation/public")]
        self.assertEqual(clean.empty_parent_targets(self.root, removals, set(), set()), expected)
        self.assertTrue(all(path.read_text() == "preserved until actual cleanup" for path in files))
        empty = self.root / "empty-checkout"
        (empty / "bin/examples").mkdir(parents=True)
        self.assertEqual(clean.empty_parent_targets(empty, [], set(), set()), [Path("bin/examples"), Path("bin")])

    def test_custom_children_and_tracked_content_preserve_their_parents(self):
        files = [self.write(name) for name in (
            ".tools/custom/cache", "bin/examples/personal-tool", "documentation/public/logo.svg")]
        selected = clean.empty_parent_targets(self.root, [], {Path("documentation/public/logo.svg")}, set())
        self.assertEqual(selected, [])
        self.assertTrue(all(path.exists() for path in files))

    def test_missing_parents_do_not_create_directories(self):
        self.assertEqual(clean.empty_parent_targets(self.root, [], set(), set()), [])
        self.assertEqual(list(self.root.iterdir()), [])

    def test_symlink_and_mount_parents_refuse_without_changes(self):
        outside = self.root / "retained"
        outside.mkdir()
        parent = self.root / ".tools"
        parent.symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink or mount"):
            clean.empty_parent_targets(self.root, [], set(), set())
        self.assertTrue(parent.is_symlink())
        parent.unlink()
        parent.mkdir()
        with self.assertRaisesRegex(ValueError, "symlink or mount"):
            clean.empty_parent_targets(self.root, [], set(), {parent})
        self.assertTrue(parent.is_dir())


class OutputPlanTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog-output-unit-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        guard = patch.object(clean, "git", side_effect=AssertionError("unit test called Git"))
        guard.start()
        self.addCleanup(guard.stop)

    def write(self, name, text="fixture"):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def test_missing_output_needs_no_directory_creation(self):
        self.assertEqual(clean.output_plan(self.root, set(), set()), ([], {}))
        self.assertFalse((self.root / "output").exists())

    def test_known_output_and_root_logs_exclude_unknown_and_private_inputs(self):
        directories = ("review", "sdk", "mutation", "ui-mutation", "property", "playwright",
                       "coverage", "verification", "ui-dependencies", "artifacts", "vscode/staging",
                       "n88/go-build-cache", "n88/trivy-cache", "deadcode-research/n16-rebuild/go-cache")
        for name in directories:
            self.write("output/" + name + "/data")
        self.write("output/commands.log")
        retained = ("demo/receipt.json", "ui-compose/private/key", "monitoring/password",
                    "sonarqube/credentials.json", "backup/dump.sql", "backups/dump.sql",
                    "native-import/receipt.json", "interoperability/installation.json",
                    "langgraph-model/model", "search-relevance/models/weights", "performance/state",
                    "personal/input.json", "vscode/keep", "n88/report.json", "settings.json")
        for name in retained:
            self.write("output/" + name)
        selected, kept = clean.output_plan(self.root, set(), set())
        self.assertEqual(set(selected), {Path("output/" + name) for name in directories} | {Path("output/commands.log")})
        self.assertEqual(len(selected), len(set(selected)))
        for name in retained:
            path = Path("output/" + name)
            self.assertTrue(any(parent == path or parent in path.parents for parent in kept), name)
        self.assertTrue(all((self.root / path).exists() for path in selected))

    def test_nested_git_markers_preserve_complete_subtrees_and_parents(self):
        for name in ("repo-file", "repo-directory", "repo-link"):
            self.write("output/review/group/" + name + "/private.txt")
        self.write("output/review/group/repo-file/.git", "gitdir: elsewhere")
        (self.root / "output/review/group/repo-directory/.git").mkdir()
        marker = self.write("original-git/config", "must stay outside selected paths")
        (self.root / "output/review/group/repo-link/.git").symlink_to(marker.parent, target_is_directory=True)
        self.write("output/review/group/report.json")
        self.write("output/review/other/result")
        selected, kept = clean.output_plan(self.root, set(), set())
        self.assertEqual(set(selected), {Path("output/review/group/report.json"), Path("output/review/other")})
        for name in ("repo-file", "repo-directory", "repo-link"):
            relative = Path("output/review/group/" + name)
            self.assertEqual(kept[relative], "nested Git repository")
            self.assertTrue(all(target != relative and target not in relative.parents for target in selected))

    def test_new_git_marker_during_partition_stops_the_plan(self):
        self.write("output/review/kept/.git", "gitdir: preserved")
        self.write("output/review/report.json")
        real_scan = os.scandir
        calls = 0
        def scan(path):
            nonlocal calls
            if Path(path) == self.root / "output/review":
                calls += 1
                if calls == 2:
                    self.write("output/review/.git", "gitdir: added during inspection")
            return real_scan(path)
        with patch.object(clean.os, "scandir", side_effect=scan):
            with self.assertRaisesRegex(ValueError, "inputs changed during inspection"):
                clean.output_plan(self.root, set(), set())
        self.assertEqual(calls, 2)
        self.assertEqual((self.root / "output/review/report.json").read_text(), "fixture")

    def test_output_root_and_intermediate_git_markers_preserve_their_siblings(self):
        self.write("output/.git", "gitdir: protected")
        self.write("output/run.log")
        self.assertEqual(clean.output_plan(self.root, set(), set()), ([], {Path("output"): "nested Git repository"}))
        (self.root / "output/.git").unlink()
        self.write("output/n88/.git", "gitdir: protected")
        self.write("output/n88/go-build-cache/file")
        selected, kept = clean.output_plan(self.root, set(), set())
        self.assertEqual(selected, [Path("output/run.log")])
        self.assertEqual(kept[Path("output/n88")], "nested Git repository")

    def test_selected_tracked_content_refuses_but_unknown_tracked_content_stays(self):
        self.write("output/review/result")
        self.write("output/personal.txt")
        selected, kept = clean.output_plan(self.root, {Path("output/personal.txt")}, set())
        self.assertEqual(selected, [Path("output/review")])
        self.assertIn(Path("output/personal.txt"), kept)
        with self.assertRaisesRegex(ValueError, "tracked content"):
            clean.output_plan(self.root, {Path("output/review/result")}, set())

    def test_selected_symlinks_are_refused_and_unknown_symlinks_are_opaque(self):
        outside = self.write("outside/keep").parent
        self.write("output/earlier.log")
        selected = self.root / "output/review"
        selected.symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink or mount"):
            clean.output_plan(self.root, set(), set())
        selected.unlink()
        (self.root / "output/personal").symlink_to(outside, target_is_directory=True)
        planned, kept = clean.output_plan(self.root, set(), set())
        self.assertEqual(planned, [Path("output/earlier.log")])
        self.assertIn(Path("output/personal"), kept)
        (self.root / "output/new.log").symlink_to(self.root / "missing")
        with self.assertRaisesRegex(ValueError, "symlink or mount"):
            clean.output_plan(self.root, set(), set())

    def test_child_links_remain_literal_in_partial_trees(self):
        outside = self.write("outside/keep").parent
        self.write("output/review/repo/.git", "gitdir: kept")
        link = self.root / "output/review/link"
        link.symlink_to(outside, target_is_directory=True)
        selected, kept = clean.output_plan(self.root, set(), set())
        self.assertEqual(selected, [Path("output/review/link")])
        self.assertIn(Path("output/review/repo"), kept)
        self.assertEqual((outside / "keep").read_text(), "fixture")

    def test_kernel_mounts_refuse_selected_targets_and_descendants(self):
        self.write("output/review/child/result")
        for name in ("output", "output/review", "output/review/child", "output/review/child/result"):
            with self.subTest(path=name), self.assertRaisesRegex(ValueError, "mount"):
                clean.output_plan(self.root, set(), {self.root / name})

    def test_invalid_types_refuse_without_reading_file_contents(self):
        self.write("output/review", "unexpected file")
        with self.assertRaisesRegex(ValueError, "unexpected file type"):
            clean.output_plan(self.root, set(), set())
        (self.root / "output/review").unlink()
        (self.root / "output/directory.log").mkdir()
        with self.assertRaisesRegex(ValueError, "unexpected file type"):
            clean.output_plan(self.root, set(), set())
        (self.root / "output/directory.log").rmdir()
        os.mkfifo(self.root / "output/pipe.log")
        with self.assertRaisesRegex(ValueError, "unexpected file type"):
            clean.output_plan(self.root, set(), set())

    def test_literal_filenames_and_console_path_escaping(self):
        name = "output/a [draft]\n\x1b[31m.log"
        self.write(name)
        selected, _ = clean.output_plan(self.root, set(), set())
        self.assertEqual(selected, [Path(name)])
        rendered = clean.display_path(selected[0])
        self.assertNotIn("\n", rendered)
        self.assertNotIn("\x1b", rendered)
        self.assertEqual(json.loads(rendered), name)
        self.assertEqual(clean.display_path(Path("output/ordinary report.log")), "output/ordinary report.log")


if __name__ == "__main__":
    unittest.main()
