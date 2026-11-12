"""Exercise cleanup against disposable Git checkouts, never this checkout's outputs."""

from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import clean as cleaner


def snapshot(root):
    """Compare fixture contents and modes independently of the cleanup inventory."""
    def fail(error):
        raise error

    result = {}
    for directory, children, files in os.walk(root, followlinks=False, onerror=fail):
        if Path(directory).resolve() == root and ".git" in children:
            children.remove(".git")
        for path in [Path(directory).resolve(), *(Path(directory).resolve() / name for name in children + files)]:
            metadata = path.lstat()
            content = os.readlink(path) if stat.S_ISLNK(metadata.st_mode) else path.read_bytes() if stat.S_ISREG(metadata.st_mode) else None
            result[str(path.relative_to(root))] = (stat.S_IMODE(metadata.st_mode), content)
    return result


def restore_fixture_permissions(root):
    """Restore owned fixture directories only, without following child links."""
    pending = [root]
    while pending:
        path = pending.pop()
        if not stat.S_ISDIR(path.lstat().st_mode):
            continue
        path.chmod(stat.S_IMODE(path.stat().st_mode) | 0o700)
        with os.scandir(path) as entries:
            pending.extend(Path(entry.path) for entry in entries if entry.is_dir(follow_symlinks=False))


def install_make(root):
    """Copy only the public Make recipes and cleaner into an owned checkout."""
    (root / "test/packaging").mkdir(parents=True, exist_ok=True)
    shutil.copy2(cleaner.ROOT / "Makefile", root / "Makefile")
    shutil.copy2(cleaner.ROOT / "go.mod", root / "go.mod")
    shutil.copy2(Path(cleaner.__file__), root / "test/packaging/clean.py")


def public_make(root, target):
    """Exercise the public command without invoking any retained checkout target."""
    return subprocess.run(["make", "--no-print-directory", target], cwd=root,
                          capture_output=True, text=True, timeout=30, check=False)


def qualify_mounts(parent, *, output=False):
    """Run only inside the caller's disposable user/mount namespace."""
    if os.geteuid() == 0:
        raise AssertionError("Mount qualification must retain an ordinary UID")
    cases = ("selected-directory", "selected-file", "ancestor", "nested-directory", "nested-file")
    for case in cases:
        base = parent / case
        root = base / "checkout space\\name"
        root.mkdir(parents=True)
        cleaner.git(root, "init", "--quiet")
        install_make(root)
        cleaner.git(root, "add", "Makefile", "test/packaging/clean.py")
        for name in (".ruff_cache/earlier", "bin/catalogctl", ".tools/ans/cache/module/readonly", "retained/keep"):
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("preserve contents")
        readonly = root / ".tools/ans/cache/module"
        readonly.chmod(0o555)
        selected_directory = "output/review" if output else "node_modules"
        if case == "selected-directory":
            destination = root / selected_directory
        elif case == "selected-file":
            destination = root / ("output/run.log" if output else "bin/catalogd")
        elif case == "ancestor":
            destination = root / ("output" if output else "catalog-ui")
        elif case == "nested-directory":
            destination = root / selected_directory / "nested"
        else:
            destination = root / selected_directory / "nested-file"
        source = base / "outside"
        if case in ("selected-file", "nested-file"):
            source.write_text("outside file sentinel")
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text("inside file sentinel")
        else:
            source.mkdir()
            (source / "keep").write_text("outside directory sentinel")
            if case == "ancestor":
                component = "review" if output else "node_modules"
                (source / component).mkdir()
                (source / component / "keep").write_text("outside dependency sentinel")
            destination.mkdir(parents=True)
            (destination / "keep").write_text("inside directory sentinel")
        source.chmod(0o640 if source.is_file() else 0o750)
        if source.stat().st_dev != destination.stat().st_dev:
            raise AssertionError("The mount control must use the same filesystem")
        original = snapshot(base)
        goals = ("clean-output-dry-run", "clean-output") if output else ("clean-dry-run", "clean")
        control = public_make(root, goals[0])
        if control.returncode != 0 or snapshot(base) != original:
            raise AssertionError(f"{case}: unchanged unmounted dry-run control failed")
        if case in ("nested-directory", "nested-file"):
            refusal = f"Refusing nested mount: {root / selected_directory}"
        else:
            refusal = f"Refusing symlink or mount: {destination.relative_to(root)}"
        mounted = False
        try:
            subprocess.run(["mount", "--bind", str(source), str(destination)], check=True, capture_output=True, timeout=10)
            mounted = True
            if destination.is_mount():
                raise AssertionError("The control must reach the same-filesystem bind gap")
            before = snapshot(base)
            for target in goals:
                result = public_make(root, target)
                if result.returncode == 0 or refusal not in result.stderr:
                    raise AssertionError(f"{case}: {target} did not refuse its mount")
                if snapshot(base) != before:
                    raise AssertionError(f"{case}: {target} changed fixture contents or modes")
        finally:
            if mounted:
                subprocess.run(["umount", str(destination)], check=True, capture_output=True, timeout=10)
            readonly.chmod(0o755)
        # Compare before teardown's deliberate permission restoration separately.
        original[str(Path(root.name) / ".tools/ans/cache/module")] = (0o755, None)
        if snapshot(base) != original:
            raise AssertionError(f"{case}: outside or selected fixture changed")
    print(json.dumps({"uid": os.geteuid(), "sameFilesystemBindCases": len(cases), "publicTargetRefusals": len(cases) * 2, "contentsAndModesPreserved": True, "unmounted": True}))


class CleanTests(unittest.TestCase):
    """Cover exact scope, source protection and filesystem redirection failures."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="catalog-clean-test-")
        self.addCleanup(self.temporary.cleanup)
        self.addCleanup(restore_fixture_permissions, Path(self.temporary.name).resolve())
        self.root = Path(self.temporary.name).resolve() / "checkout"
        self.root.mkdir()
        cleaner.git(self.root, "init", "--quiet")
        self.write(".gitignore", "node_modules/\noutput/\nbin/\ncoverage/\n__pycache__/\n")
        self.write("test/example.py", "# maintained source\n")
        cleaner.git(self.root, "add", ".gitignore", "test/example.py")

    def write(self, relative, text="keep"):
        """Create a fixture under the disposable checkout only."""
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def run_clean(self, **kwargs):
        """Capture normal progress output for concise test results."""
        with redirect_stdout(io.StringIO()) as output:
            result = cleaner.clean(self.root, **kwargs)
        return result, output.getvalue()

    def test_all_maintained_npm_projects_have_dependency_cleanup(self):
        """Include new non-ignored project inputs before they reach the index."""
        manifests = [os.fsdecode(name) for name in cleaner.git(
            cleaner.ROOT, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "**/package.json").split(b"\0") if name]
        maintained = [name for name in manifests if not name.startswith(("schemas/upstream/", "samples/upstream/"))]
        self.assertTrue(maintained, "No maintained npm manifests discovered")
        for manifest in maintained:
            with self.subTest(manifest=manifest):
                self.assertIn(str(Path(manifest).parent / "node_modules"), cleaner.TARGETS)

    def test_literal_npm_projects_and_receiver_binary_through_make(self):
        """Qualify producer paths without building fixtures from TARGETS."""
        install_make(self.root)
        projects = ("catalog-ui", "documentation", "sdk/typescript", "extensions/vscode",
                    "test/validation", "test/interoperability/mcp", "examples/typescript-ard-list", "test/ci/markdown")
        removed = [self.write(project + "/node_modules/package/index.js", "disposable dependency") for project in projects]
        binaries = ("bin/examples/publication-report", "bin/examples/mcp-scan-receiver")
        source = (cleaner.ROOT / "Makefile").read_text()
        for binary, prefix in zip(binaries, ("../../", "../../../"), strict=True):
            self.assertIn("-o " + prefix + binary, source)
            removed.append(self.write(binary, "disposable receiver executable"))
        removed += [self.write(name, "disposable PDF artifact") for name in (
            ".tools/documentation-pdf/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell", "output/documentation-pdf/catalog-manual.pdf")]
        retained = [self.write(project + "/" + name, "retained package input")
                    for project in projects for name in ("package.json", "package-lock.json")]
        retained += [self.write(name, "retained input") for name in (
            "bin/examples/personal-tool", ".venv/bin/python", "output/review/report.json",
            "output/demo/receipt.json", "output/ui-compose/development/inputs/config.json")]
        cleaner.git(self.root, "add", "--", *[project + "/package.json" for project in projects])
        for path in removed + retained:
            path.chmod(0o640)
        before = snapshot(self.root)
        preview = public_make(self.root, "clean-dry-run")
        self.assertEqual(preview.returncode, 0, preview.stderr)
        for target in (*[project + "/node_modules" for project in projects], *binaries):
            self.assertIn("Would remove " + target + "\n", preview.stdout)
        self.assertEqual(snapshot(self.root), before)
        result = public_make(self.root, "clean")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(all(not path.exists() for path in removed))
        self.assertTrue(all(not (self.root / project / "node_modules").exists() for project in projects))
        after = snapshot(self.root)
        for path in retained:
            relative = str(path.relative_to(self.root))
            self.assertEqual(after[relative], before[relative])
        repeated = public_make(self.root, "clean")
        self.assertEqual(repeated.returncode, 0, repeated.stderr)
        self.assertEqual(snapshot(self.root), after)
        self.assertIn("No known build artifacts", repeated.stdout)

    def test_extension_scope_preserves_new_npm_and_receiver_outputs(self):
        removed_by_root = [self.write(name, "root-only disposable output") for name in (
            "test/interoperability/mcp/node_modules/package/index.js",
            "test/ci/markdown/node_modules/package/index.js",
            "examples/typescript-ard-list/node_modules/package/index.js", "bin/examples/publication-report", "bin/examples/mcp-scan-receiver")]
        for path in removed_by_root:
            path.chmod(0o640)
        before = snapshot(self.root)
        self.run_clean(scope="vscode", dry_run=True)
        self.assertEqual(snapshot(self.root), before)
        self.run_clean(scope="vscode")
        self.assertEqual(snapshot(self.root), before)

    def test_inventory_dry_run_deletion_and_repeat_preserve_other_files(self):
        removed = []
        files = {"bin/catalogd", "bin/catalogctl", "bin/catalog-ui", "bin/examples/publication-report", "bin/examples/mcp-scan-receiver", "httpserver.test",
                 "coverage/default.out", "coverage/core.out", "coverage/postgres.out",
                 "coverage/unit.out", "coverage/index.html", "coverage/process-postgres.out",
                 "coverage/full.out", "coverage/full.txt", "coverage/publication-report-unit.out",
                 "coverage/mcp-scanner-receiver-unit.out", "extensions/vscode/THIRD_PARTY_NOTICES.txt"}
        for relative in cleaner.TARGETS:
            removed.append(self.write(relative if relative in files else relative + "/generated"))
        removed.append(self.write("test/__pycache__/example.cpython-312.pyc"))
        retained = [self.write(name) for name in (
            ".env", ".env.private", "go.work", "bin/personal-tool", "coverage/measurements/result.json",
            "output/performance/state.json", "output/artifacts-custom/package.tgz",
            "output/embeddings/models/weights", "output/playwright/result.png", "output/backup/dump.sql",
            "output/n88/result.json", "output/deadcode-research/n16-rebuild/check.log",
            "output/demo/catalog-demo-v1.json", "output/demo/archives/cert.pem",
            "output/ui-compose/development/inputs/config.json",
            "output/monitoring/development/grafana-password", "output/sonarqube/credentials.json",
            "output/search-relevance/model/weights", "output/old-coverage/before.out",
            "objects/document", "objects-dev/document", ".venv/bin/python",
            "catalog-ui/tests/browser/snapshots/expected.png", "documentation/assets/screenshot.png",
            "documentation/src/content/docs/guide.md", "documentation/public/logo.svg", "schemas/upstream/profile/schema.json",
            "package-lock.json", "pyproject.toml", "uv.lock", "VERSION", ".tools/personal/tool", "local-untracked-notes.txt",
            "sdk/go/client.gen.go", "sdk/go/sdk.go", "sdk/go/go.mod", "sdk/go/go.sum",
            "sdk/python/pyproject.toml", "sdk/python/src/catalog_sdk/generated/api_client.py",
            "test/sdk/python/uv.lock", "test/sdk/python/pyproject.toml",
            "sdk/typescript/package.json", "sdk/typescript/package-lock.json", "sdk/typescript/src/schema.d.ts",
            "sdk/rust/Cargo.toml", "sdk/rust/Cargo.lock", "sdk/rust/src/generated/client.rs", "sdk/rust/src/transport.rs",
            "test/sdk/rust/consumer/Cargo.toml", "test/sdk/rust/consumer/Cargo.lock", "test/sdk/rust/generator.lock",
            "output/sdk-rust-advisories/advisory-db/README.md",
            "extensions/vscode/package.json", "extensions/vscode/package-lock.json", "extensions/vscode/src/extension.ts",
            "test/vscode/runner.py",
        )]
        cleaner.git(self.root, "add", "documentation/public/logo.svg")
        before, output = self.run_clean(dry_run=True)
        self.assertIn("Would remove documentation/node_modules", output)
        self.assertIn("Would remove extensions/vscode/node_modules", output)
        self.assertIn("Would remove extensions/vscode/THIRD_PARTY_NOTICES.txt", output)
        self.assertIn("Would remove test/vscode/.tools", output)
        self.assertIn("Would remove .tools/ci", output)
        self.assertTrue(all(path.exists() for path in removed + retained))
        actual, _ = self.run_clean()
        self.assertEqual(actual, before)
        self.assertTrue(all(not path.exists() for path in removed))
        self.assertTrue(all(path.exists() for path in retained))
        self.assertTrue((self.root / "test/example.py").exists())
        self.assertEqual(self.run_clean()[0], [])

    def test_make_removes_default_test_binary_and_empty_build_parents(self):
        """Preview and remove actual build paths without touching local state."""
        install_make(self.root)
        for name in ("httpserver.test", ".tools/dev/installed.json", "bin/catalogd",
                     "bin/examples/publication-report", "documentation/public/generated/manual.json"):
            self.write(name, "disposable build output")
        retained = [self.write(name, "local state") for name in (
            ".venv/bin/python", "logger.log", ".codex/settings.json",
            "output/demo/qualification/receipt.json", "output/ui-compose/old-project/inputs/key")]
        before = snapshot(self.root)
        preview = public_make(self.root, "clean-dry-run")
        self.assertEqual(preview.returncode, 0, preview.stderr)
        for name in ("httpserver.test", ".tools", "bin/examples", "bin", "documentation/public"):
            self.assertIn("Would remove " + name + "\n", preview.stdout)
        self.assertEqual(snapshot(self.root), before)
        result = public_make(self.root, "clean")
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in ("httpserver.test", ".tools", "bin", "documentation/public"):
            self.assertFalse((self.root / name).exists(), name)
        after = snapshot(self.root)
        for path in retained:
            relative = str(path.relative_to(self.root))
            self.assertEqual(after[relative], before[relative])
        self.assertEqual(public_make(self.root, "clean").returncode, 0)
        self.assertEqual(snapshot(self.root), after)

    def test_empty_parent_removal_preserves_late_created_files(self):
        """An unexpected writer cannot turn empty-parent pruning into recursion."""
        (self.root / ".tools").mkdir()
        original_plan = cleaner.plan

        def select(root, *, scope):
            result = original_plan(root, scope=scope)
            self.write(".tools/personal-tool", "created after preflight")
            return result

        with patch.object(cleaner, "plan", side_effect=select):
            with self.assertRaises(OSError):
                self.run_clean()
        self.assertEqual((self.root / ".tools/personal-tool").read_text(), "created after preflight")

    def test_tracked_content_aborts_the_whole_plan(self):
        sentinel = self.write("bin/catalogd")
        tracked = self.write("documentation/dist/keep.md")
        cleaner.git(self.root, "add", "documentation/dist/keep.md")
        with self.assertRaisesRegex(ValueError, "tracked content"):
            self.run_clean()
        self.assertTrue(sentinel.exists())
        self.assertTrue(tracked.exists())

    def test_literal_coverage_and_python_caches_preserve_retained_outputs_and_vscode_scope(self):
        """Exercise the missing names independently of the cleaner's inventory."""
        install_make(self.root)
        files = ("coverage/process-postgres.out", "coverage/full.out", "coverage/full.txt",
                 "coverage/publication-report-unit.out", "coverage/mcp-scanner-receiver-unit.out")
        directories = (".mypy_cache", ".hypothesis", "coverage/.staging")
        removed = [self.write(name, "disposable profile") for name in files]
        removed += [self.write(name + "/owned-scratch", "disposable cache") for name in directories]
        removed.append(self.write("output/artifacts/custom/package.zip", "custom path inside inventory"))
        retained = [self.write(name, "retained evidence") for name in (
            ".env", "go.work", "package-lock.json", "coverage/historical.out",
            "coverage/performance/result.json", "coverage/unit.legacy/unknown",
            "output/custom-packages/package.zip", "output/custom-cache/constants",
            "output/playwright/report.json", "output/demo/receipt.json",
            "output/tutorials/private/token", ".venv/bin/python", ".tools/personal/cache",
        )]
        for path in removed + retained:
            path.chmod(0o640)
        before = snapshot(self.root)
        result = public_make(self.root, "clean-dry-run")
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in (*files, *directories, "output/artifacts"):
            self.assertIn("Would remove " + name + "\n", result.stdout)
        self.assertEqual(snapshot(self.root), before)
        self.run_clean(scope="vscode", dry_run=True)
        self.run_clean(scope="vscode")
        self.assertEqual(snapshot(self.root), before)
        result = public_make(self.root, "clean")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(all(not path.exists() for path in removed))
        after = snapshot(self.root)
        for path in retained:
            name = str(path.relative_to(self.root))
            self.assertEqual(after[name], before[name])
        repeated = public_make(self.root, "clean")
        self.assertEqual(repeated.returncode, 0, repeated.stderr)
        self.assertIn("No known build artifacts", repeated.stdout)

    def test_literal_installations_and_rust_report_target_readonly_cache_and_links_through_make(self):
        """Inventory completeness must not come from the implementation's tuple."""
        install_make(self.root)
        literal = ("examples/langgraph/.venv", ".tools/gitleaks", ".tools/agntcy", ".tools/gateway", ".tools/ans", "examples/rust-catalog-report/target", "test/mcp-scanner/.tools")
        for name in literal:
            self.write(name + "/installed.json", "owned installation").chmod(0o640)
        retained = [self.write(name, "retained input") for name in (
            ".env", ".tools/personal/tool", ".venv/bin/python", "examples/other/.venv/bin/python",
            "examples/langgraph/uv.lock", "examples/rust-catalog-report/Cargo.lock", "examples/rust-catalog-report/src/main.rs", "examples/other/target/keep", "output/gateway/report.json", "output/langgraph-model/weights", "test/mcp-scanner/requirements.txt",
            "output/playwright/session/profile", "output/tutorials/private/token", "output/demo/receipt.json",
        )]
        for path in retained:
            path.chmod(0o600)
        self.write("examples/langgraph/.venv/lib/package.py")
        (self.root / "examples/langgraph/.venv/lib64").symlink_to("lib", target_is_directory=True)
        outside = Path(self.temporary.name).resolve() / "outside"
        outside.mkdir()
        (outside / "keep").write_text("outside sentinel")
        (outside / "keep").chmod(0o440)
        outside.chmod(0o750)
        (self.root / ".tools/ans/external").symlink_to(outside, target_is_directory=True)
        readonly = self.write(".tools/ans/gopath/pkg/mod/example@v1/subtree/source.go", "module content")
        readonly.chmod(0o444)
        for path in (readonly.parent, readonly.parent.parent):
            path.chmod(0o555)
        before = snapshot(Path(self.temporary.name).resolve())
        dry = public_make(self.root, "clean-dry-run")
        self.assertEqual(dry.returncode, 0, dry.stderr)
        for name in literal:
            self.assertIn("Would remove " + name, dry.stdout)
        self.assertEqual(snapshot(Path(self.temporary.name).resolve()), before)
        removed = public_make(self.root, "clean")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertTrue(all(not (self.root / name).exists() for name in literal))
        for path in retained + [outside, outside / "keep"]:
            relative = str(path.relative_to(self.root.parent))
            self.assertEqual(snapshot(Path(self.temporary.name).resolve())[relative], before[relative])
        repeated = public_make(self.root, "clean")
        self.assertEqual(repeated.returncode, 0, repeated.stderr)
        self.assertIn("No known build artifacts", repeated.stdout)

    def test_unreadable_selected_and_descendant_refuse_both_make_targets(self):
        """Unprivileged traversal errors cannot hide a nested repository."""
        if os.geteuid() == 0:
            self.skipTest("Unreadable-directory controls require an ordinary UID")
        install_make(self.root)
        self.write(".ruff_cache/earlier", "earlier selected sentinel")
        self.write("bin/catalogd", "earlier binary sentinel")
        readonly = self.write(".tools/ans/cache/module/source.go", "readonly sentinel").parent
        readonly.chmod(0o555)
        self.write("node_modules/sealed/.git/config", "nested repository sentinel")
        outside = Path(self.temporary.name).resolve() / "outside"
        outside.write_text("outside sentinel")
        outside.chmod(0o400)
        for name in ("node_modules", "node_modules/sealed"):
            with self.subTest(path=name):
                inaccessible = self.root / name
                before = snapshot(Path(self.temporary.name).resolve())
                mode = stat.S_IMODE(inaccessible.stat().st_mode)
                inaccessible.chmod(0)
                try:
                    for target in ("clean-dry-run", "clean"):
                        result = public_make(self.root, target)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("Permission denied", result.stderr)
                        self.assertEqual(stat.S_IMODE(inaccessible.stat().st_mode), 0)
                        self.assertEqual(stat.S_IMODE(readonly.stat().st_mode), 0o555)
                        self.assertEqual((self.root / ".ruff_cache/earlier").read_text(), "earlier selected sentinel")
                        self.assertEqual((self.root / "bin/catalogd").read_text(), "earlier binary sentinel")
                        self.assertEqual(outside.read_text(), "outside sentinel")
                finally:
                    inaccessible.chmod(mode)
                self.assertEqual(snapshot(Path(self.temporary.name).resolve()), before)
        readonly.chmod(0o755)

    def test_mount_inventory_decodes_paths_and_fails_closed(self):
        sentinel = self.write("bin/catalogd", "must survive mount inspection failure")
        sentinel.chmod(0o600)
        inventory = self.write("mountinfo", "12 1 8:1 / /tmp/a\\040b\\134c rw - ext4 /dev/a rw\n")
        with patch.object(cleaner, "MOUNTINFO", inventory):
            self.assertEqual(cleaner.mounted_paths(), {Path("/tmp/a b\\c")})
            for text in ("", "malformed\n", "12 1 8:1 / relative rw - ext4 /dev/a rw\n"):
                inventory.write_text(text)
                with self.assertRaisesRegex(ValueError, "mount inventory"):
                    self.run_clean()
            if os.geteuid() != 0:
                inventory.chmod(0)
                try:
                    with self.assertRaises(PermissionError):
                        self.run_clean()
                finally:
                    inventory.chmod(0o600)
        with patch.object(cleaner, "MOUNTINFO", self.root / "absent"):
            with self.assertRaises(FileNotFoundError):
                self.run_clean()
        with patch.object(cleaner.sys, "platform", "darwin"):
            with self.assertRaisesRegex(ValueError, "requires Linux"):
                self.run_clean()
        self.assertEqual(sentinel.read_text(), "must survive mount inspection failure")
        self.assertEqual(stat.S_IMODE(sentinel.stat().st_mode), 0o600)

    def test_simulated_selected_ancestor_and_nested_file_mounts(self):
        self.write(".ruff_cache/earlier")
        self.write("bin/catalogd")
        self.write("catalog-ui/node_modules/package/file")
        self.write("node_modules/package/file")
        before = snapshot(self.root)
        for name in ("bin/catalogd", "catalog-ui", "node_modules/package/file"):
            with self.subTest(path=name), patch.object(cleaner, "mounted_paths", return_value={self.root / name}):
                with self.assertRaisesRegex(ValueError, "mount"):
                    self.run_clean()
                self.assertEqual(snapshot(self.root), before)

    def test_real_same_filesystem_bind_mounts_refuse_both_make_targets(self):
        """Real kernel mounts qualify behavior independently of simulated controls."""
        tools = ("unshare", "mount", "umount")
        if sys.platform != "linux" or any(shutil.which(tool) is None for tool in tools):
            self.skipTest("Real bind qualification requires Linux unshare/mount/umount")
        namespace = ["unshare", "--user", "--map-current-user", "--mount", "--keep-caps", "--propagation", "private"]
        available = subprocess.run([*namespace, "true"], capture_output=True, text=True, timeout=10, check=False)
        if available.returncode != 0:
            self.skipTest("Owned user/mount namespace unavailable: " + available.stderr.strip())
        result = subprocess.run([*namespace, sys.executable, "-B", __file__, "--qualify-mounts", str(self.root.parent)],
                                capture_output=True, text=True, timeout=180, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        facts = json.loads(result.stdout)
        self.assertEqual(facts["sameFilesystemBindCases"], 5)
        self.assertEqual(facts["publicTargetRefusals"], 10)
        self.assertTrue(facts["contentsAndModesPreserved"])
        self.assertTrue(facts["unmounted"])
        print("Real mount qualification: " + result.stdout.strip())

    def test_target_or_ancestor_symlinks_are_rejected(self):
        for relative in ("documentation", "documentation/dist", "bin/catalogd", ".tools", ".tools/dev", ".tools/ci"):
            with self.subTest(relative=relative):
                link = self.root / relative
                link.parent.mkdir(parents=True, exist_ok=True)
                outside = Path(self.temporary.name).resolve() / "external"
                outside.mkdir(exist_ok=True)
                sentinel = outside / "important"
                sentinel.write_text("keep")
                link.symlink_to(outside, target_is_directory=True)
                with self.assertRaisesRegex(ValueError, "symlink or mount"):
                    self.run_clean()
                self.assertTrue(sentinel.exists())
                link.unlink()
        broken = self.root / "node_modules"
        broken.symlink_to(self.root / "missing")
        with self.assertRaisesRegex(ValueError, "symlink or mount"):
            self.run_clean()

    def test_symlink_inside_artifacts_never_deletes_destination(self):
        outside = self.write("important/input.txt")
        dependencies = self.root / "node_modules"
        dependencies.mkdir()
        (dependencies / "linked-package").symlink_to(outside.parent, target_is_directory=True)
        self.run_clean()
        self.assertEqual(outside.read_text(), "keep")
        self.assertFalse(dependencies.exists())

    def test_unexpected_target_types_are_not_recursively_removed(self):
        """A directory at a binary path must not become a recursive deletion target."""
        nested = self.write("bin/catalogd/personal-file")
        with self.assertRaisesRegex(ValueError, "unexpected file type"):
            self.run_clean()
        self.assertTrue(nested.exists())
        nested.unlink()
        nested.parent.rmdir()
        sentinel = self.write("node_modules", "unexpected file")
        with self.assertRaisesRegex(ValueError, "unexpected file type"):
            self.run_clean()
        self.assertEqual(sentinel.read_text(), "unexpected file")

    def test_nested_repository_and_mount_are_rejected_before_deletion(self):
        sentinel = self.write("bin/catalogd")
        nested = self.write("node_modules/custom/.git", "gitdir: elsewhere")
        with self.assertRaisesRegex(ValueError, "nested repository"):
            self.run_clean()
        nested.unlink()
        original = Path.is_mount
        mounted = nested.parent
        with patch.object(Path, "is_mount", lambda path: path == mounted or original(path)):
            with self.assertRaisesRegex(ValueError, "nested mount"):
                self.run_clean()
        self.assertTrue(sentinel.exists())

    def test_wrong_root_and_git_failure_do_not_delete(self):
        sentinel = self.write("bin/catalogd")
        child = self.root / "child"
        child.mkdir()
        with self.assertRaisesRegex(ValueError, "repository root"):
            cleaner.clean(child)
        with patch.object(cleaner, "git", side_effect=subprocess.CalledProcessError(1, "git")):
            with self.assertRaises(subprocess.CalledProcessError):
                self.run_clean()
        self.assertTrue(sentinel.exists())

    def test_actual_make_targets_ignore_workflow_and_run_from_script_checkout(self):
        """Exercise both public recipes with no dependency on installed npm tools."""
        install_make(self.root)
        self.write(".env", "$(error Cleanup must not read this file)\n")
        self.write("test/validation/node_modules/package/index.js")
        self.write("bin/catalogd")
        for target in ("clean-dry-run", "clean", "clean"):
            result = subprocess.run(
                ["make", "--no-print-directory", target], cwd=self.root,
                capture_output=True, text=True, timeout=30, check=True,
            )
            self.assertNotIn("Cleanup must not read this file", result.stderr)
            if target == "clean-dry-run":
                self.assertTrue((self.root / "bin/catalogd").exists())
            else:
                self.assertFalse((self.root / "bin/catalogd").exists())
        self.assertTrue((self.root / ".env").exists())


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] in ("--qualify-mounts", "--qualify-output-mounts"):
        qualify_mounts(Path(sys.argv[2]), output=sys.argv[1] == "--qualify-output-mounts")
    else:
        unittest.main()
