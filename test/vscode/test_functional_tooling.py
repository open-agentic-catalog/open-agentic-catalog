"""Actual Make/build/archive/watch controls in a disposable checkout with spaces."""
import importlib.util
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
import zipfile

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("vscode_build_controls", ROOT / "extensions/vscode/tooling/runner.py")
core = importlib.util.module_from_spec(spec)
spec.loader.exec_module(core)


class ToolingFunctionalTests(unittest.TestCase):
    def test_actual_make_package_failure_watch_and_scoped_cleanup(self):
        stamp = core.installed()
        with tempfile.TemporaryDirectory(prefix="vscode-tooling-", dir=ROOT / "output/vscode") as temporary:
            root = Path(temporary) / "checkout with spaces"
            root.mkdir()
            paths = set(core.source_hashes()) | set(core.inputs()) | {".gitignore", "test/sdk/typescript_runner.py", "test/sdk/node_runtime.py", "test/sdk/node-runtimes.json", "sdk/typescript/.nvmrc", "output/artifacts/sdk/typescript/manifest.json"}
            for relative in paths:
                source = ROOT / relative
                target = root / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, target)
            ext = root / "extensions/vscode"
            shutil.copytree(core.EXTENSION / "node_modules", ext / "node_modules")
            shutil.copytree(core.TOOLS, ext / ".tools")
            state = json.loads((ext / ".tools/installed.json").read_text())
            for key in ("node", "npm"):
                state[key] = str(root / Path(state[key]).relative_to(ROOT))
            (ext / ".tools/installed.json").write_text(json.dumps(state))
            env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
            env.update(NODE_OPTIONS="--require=/missing/ambient-hook.cjs", NPM_CONFIG_REGISTRY="http://127.0.0.1:1", NPM_CONFIG_USERCONFIG="/missing/ambient-npmrc")
            poison = root / "poison.env"
            poison.write_text("$(error A VS Code target loaded the workflow env file)\n")

            def make(target, *, local=True, success=True):
                command = ["make", "--no-print-directory", "-C", str(ext if local else root), target, "PYTHON=" + sys.executable, "CATALOG_ENV_FILE=" + str(poison)]
                result = subprocess.run(command, cwd=Path(temporary), env=env, capture_output=True, text=True, timeout=150)
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                return result

            subprocess.run(["git", "init", "--quiet", str(root)], check=True, env={key: value for key, value in env.items() if not key.startswith("GIT_")})
            subprocess.run(["git", "-C", str(root), "add", "."], check=True, stdout=subprocess.DEVNULL)
            self.assertFalse((root / "test/sdk/.tools").exists())
            self.assertFalse((root / "test/vscode/.tools").exists())
            self.assertIn("test-tools-install", make("help").stdout)
            self.assertIn("Select the local editor", make("debug", success=False).stderr)
            make("check")
            shutil.rmtree(ext / "dist/test")
            make("build")
            self.assertTrue((ext / "dist/extension.cjs.map").is_file())
            self.assertFalse((ext / "dist/test").exists())
            make("package")
            artifacts = root / "output/artifacts/vscode"
            manifest = artifacts / "manifest.json"
            vsix = artifacts / json.loads(manifest.read_text())["artifact"]
            original = vsix.read_bytes()
            original_manifest = manifest.read_bytes()

            # Inspect the collector's real output, including nested runtime notices.
            undici = ext / "node_modules/undici"
            undici_metadata = json.loads((undici / "package.json").read_text())
            nested_notices = ("lib/web/fetch/LICENSE", "lib/milo/LICENSE.md")
            with zipfile.ZipFile(vsix) as archive:
                notices = archive.read("extension/THIRD_PARTY_NOTICES.txt")
                root_notice = f"undici@{undici_metadata['version']} ({undici_metadata['license']})\n".encode() + (undici / "LICENSE").read_bytes()
                self.assertIn(root_notice, notices)
                for relative in nested_notices:
                    expected = f"undici@{undici_metadata['version']} - {relative}\n".encode() + (undici / relative).read_bytes()
                    self.assertIn(expected, notices)
                self.assertEqual(archive.read("extension/LICENSE.txt"), (ext / "LICENSE").read_bytes())
                self.assertEqual(archive.read("extension/NOTICE"), (ext / "NOTICE").read_bytes())

            # Missing or empty required notices must not replace a complete pair.
            for relative in nested_notices:
                path = undici / relative
                complete = path.read_bytes()
                for broken in (None, b""):
                    with self.subTest(notice=relative, missing=broken is None):
                        try:
                            if broken is None:
                                path.unlink()
                            else:
                                path.write_bytes(broken)
                            result = make("package", success=False)
                            self.assertIn("required bundled notice", result.stderr)
                            self.assertIn("undici/" + relative, result.stderr)
                            self.assertEqual(vsix.read_bytes(), original)
                            self.assertEqual(manifest.read_bytes(), original_manifest)
                            self.assertEqual(json.loads((root / "output/vscode/package-attempt.json").read_text())["status"], "failed")
                            self.assertEqual(list((root / "output/vscode/staging").glob("package-*/")), [])
                        finally:
                            path.write_bytes(complete)
            make("vscode-package", local=False)
            self.assertEqual(vsix.read_bytes(), original)
            make("verify")
            vsix.write_bytes(original + b"owned replacement canary")
            make("verify", success=False)
            vsix.write_bytes(original)
            self.assertFalse((ext / "dist/test").exists(), "Package-only build compiled tests")
            source = ext / "src/config.ts"
            baseline = source.read_text()
            source.write_text(baseline.replace("Catalog: request_failed.", "Catalog: owned-source-change."))
            make("package")
            self.assertNotEqual(vsix.read_bytes(), original)
            source.write_text(baseline)
            make("package")
            self.assertEqual(vsix.read_bytes(), original)
            original_manifest = manifest.read_bytes()

            # A real VSCE refusal and a real closed-list inspection refusal.
            guide = ext / "README.md"
            original_guide = guide.read_text()
            guide.write_text(original_guide + '\n<svg xmlns="http://www.w3.org/2000/svg"></svg>\n')
            make("package", success=False)
            self.assertEqual(vsix.read_bytes(), original)
            self.assertEqual(manifest.read_bytes(), original_manifest)
            guide.write_text(original_guide)
            builder = ext / "tooling/build.mjs"
            original_builder = builder.read_text()
            builder.write_text(original_builder + '\nif(mode === "production") await writeFile(resolve(destination,"unexpected.txt"),"owned canary");\n')
            self.assertIn("unexpected members", make("package", success=False).stderr)
            self.assertEqual(vsix.read_bytes(), original)
            self.assertEqual(manifest.read_bytes(), original_manifest)
            builder.write_text(original_builder)

            # Change a captured source after the actual compiler has completed.
            log_path = root / "late-package.log"
            with log_path.open("w") as output:
                process = subprocess.Popen(["make", "--no-print-directory", "-C", str(ext), "package", "PYTHON=" + sys.executable], env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
                try:
                    deadline = time.monotonic() + 30
                    while not list((root / "output/vscode/staging").glob("package-*/extension/dist/extension.cjs")):
                        if process.poll() is not None or time.monotonic() > deadline:
                            self.fail("Late-change fixture did not reach compiled staging: " + log_path.read_text())
                        time.sleep(0.01)
                    source.write_text(baseline + "\n// Owned late source change.\n")
                    self.assertNotEqual(process.wait(timeout=30), 0)
                finally:
                    if process.poll() is None:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=10)
                    source.write_text(baseline)
            self.assertIn("inputs changed", log_path.read_text())
            self.assertEqual(vsix.read_bytes(), original)
            self.assertEqual(manifest.read_bytes(), original_manifest)
            self.assertEqual(list((root / "output/vscode/staging").glob("package-*/")), [])

            # Interrupt a real staged build and require cleanup plus old output.
            with (root / "interrupted-package.log").open("w") as output:
                process = subprocess.Popen(["make", "--no-print-directory", "-C", str(ext), "package", "PYTHON=" + sys.executable], env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
                try:
                    deadline = time.monotonic() + 30
                    while not list((root / "output/vscode/staging").glob("package-*/extension")):
                        if process.poll() is not None or time.monotonic() > deadline:
                            self.fail("Interrupted build did not enter staging")
                        time.sleep(0.01)
                    os.killpg(process.pid, signal.SIGINT)
                    self.assertNotEqual(process.wait(timeout=15), 0)
                finally:
                    if process.poll() is None:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=10)
            self.assertEqual(vsix.read_bytes(), original)
            self.assertEqual(manifest.read_bytes(), original_manifest)
            self.assertEqual(list((root / "output/vscode/staging").glob("package-*/")), [])

            # A real compiler refusal must preserve the previous complete pair.
            source.write_text(baseline + '\nconst broken: number = "not a number";\n')
            make("package", success=False)
            self.assertEqual(vsix.read_bytes(), original)
            self.assertEqual(manifest.read_bytes(), original_manifest)
            self.assertEqual(json.loads((root / "output/vscode/package-attempt.json").read_text())["status"], "failed")
            source.write_text(baseline)

            # Inspect copied archives against the successful staged payload.
            stage = root / "inspection-inputs"
            stage.mkdir()
            with zipfile.ZipFile(vsix) as archive:
                payload = {name: archive.read(name) for name in archive.namelist()}
            for name, relative in core.PAYLOAD.items():
                destination = stage / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(payload[name])
            copied = root / "control.vsix"
            for change in ({}, {"extension/unexpected": b"x"}, {"extension/../canary": b"x"}, {"extension/dist/extension.cjs": None}, {"extension/readme.md": b"changed"}, {"extension/media/catalog.js": b"changed"}, {"extension.vsixmanifest": payload["extension.vsixmanifest"].replace(b'Publisher="open-agentic-catalog"', b'Publisher="wrong"')}):
                with zipfile.ZipFile(copied, "w") as archive:
                    for name, value in (payload | change).items():
                        if value is not None:
                            archive.writestr(name, value)
                if change:
                    with self.assertRaises(RuntimeError):
                        core.inspect_package(copied, stage)
                else:
                    core.inspect_package(copied, stage)
            for duplicate, symlink in ((True, False), (False, True)):
                with zipfile.ZipFile(copied, "w") as archive:
                    for name, value in payload.items():
                        info = zipfile.ZipInfo(name)
                        info.external_attr = ((0o120777 if symlink and name.endswith("catalog.js") else 0o100644) << 16)
                        archive.writestr(info, value)
                    if duplicate:
                        archive.writestr("extension/package.json", payload["extension/package.json"])
                with self.assertRaises(RuntimeError):
                    core.inspect_package(copied, stage)

            # Relocate the same owned checkout and rebind only tool locations.
            relocated = root.with_name("relocated checkout with spaces")
            root.rename(relocated)
            previous = root
            root = relocated
            ext = root / "extensions/vscode"
            poison = root / "poison.env"
            state = json.loads((ext / ".tools/installed.json").read_text())
            for key in ("node", "npm"):
                state[key] = str(root / Path(state[key]).relative_to(previous))
            (ext / ".tools/installed.json").write_text(json.dumps(state))
            make("package")
            vsix = root / vsix.relative_to(previous)
            manifest = root / manifest.relative_to(previous)
            self.assertEqual(vsix.read_bytes(), original)
            source = ext / "src/config.ts"
            log = root / "watch.log"
            with log.open("w") as output:
                watch = subprocess.Popen(["make", "--no-print-directory", "-C", str(ext), "watch", "PYTHON=" + sys.executable], env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
                def until(text, after=0):
                    deadline = time.monotonic() + 40
                    while time.monotonic() < deadline:
                        data = log.read_text()
                        if text in data[after:]:
                            return len(data)
                        if watch.poll() is not None:
                            self.fail(data)
                        time.sleep(0.1)
                    self.fail("Watch deadline: " + log.read_text())
                try:
                    position = until("[watch] build finished")
                    make("package", success=False)
                    self.assertIn("active", make("clean-dry-run", success=False).stderr)
                    source.write_text(baseline + '\nconst broken: number = "wrong";\n')
                    position = until("[watch] build failed", position)
                    source.write_text(baseline)
                    until("[watch] build finished", position)
                finally:
                    os.killpg(watch.pid, signal.SIGINT)
                    try:
                        watch.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(watch.pid, signal.SIGKILL)
                        watch.wait(timeout=10)
            self.assertNotEqual(watch.returncode, 0, "Interrupted watch claimed a normal completed build")
            preserved = [root / name for name in ("sdk/typescript/node_modules/sentinel", "test/sdk/.tools/sentinel", "output/vscode/keep-report.json", "user-profile/sentinel", "installed-resources/sentinel", "catalog-ui/node_modules/sentinel")]
            for path in preserved:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("preserve")
            before = vsix.read_bytes()
            make("clean-dry-run")
            self.assertEqual(vsix.read_bytes(), before)
            make("clean")
            self.assertFalse(vsix.exists())
            self.assertFalse((ext / ".tools").exists())
            self.assertTrue(all(path.read_text() == "preserve" for path in preserved))
            self.assertIn("build tools", make("package", success=False).stderr)
            self.assertFalse((ext / ".tools").exists(), "A check acquired missing tools")
            self.assertTrue(Path(stamp["node"]).is_file(), "Fixture cleanup removed retained tools")


if __name__ == "__main__":
    unittest.main()
