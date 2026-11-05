"""SDK selection and packaging failures with injected tools, never subprocesses."""
from contextlib import redirect_stdout, redirect_stderr
import io
import json
from pathlib import Path
import subprocess
import tempfile
import tarfile
import unittest
import zipfile
from unittest.mock import patch

import runner
import python_runner
import typescript_runner


class PackageUnits(unittest.TestCase):
    def test_each_sdk_archive_requires_its_own_exact_license_and_notice(self):
        for filename, prefix in (("sdk.whl", "open_agentic_catalog-0.1.0.dist-info/licenses/"),
                                 ("sdk.tar.gz", "open_agentic_catalog-0.1.0/"),
                                 ("sdk.tgz", "package/"), ("sdk.crate", "open-agentic-catalog-0.1.0/")):
            with self.subTest(filename=filename), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                sdk = root / "sdk"
                sdk.mkdir()
                texts = {"LICENSE": b"SDK project license\n", "NOTICE": b"SDK generator attribution\n"}
                for name, text in texts.items():
                    (sdk / name).write_bytes(text)
                archive = root / filename

                def build(changes):
                    files = {prefix + name: text for name, text in texts.items()}
                    files.update(changes)
                    if filename.endswith(".whl"):
                        with zipfile.ZipFile(archive, "w") as package:
                            for name, text in files.items():
                                if text is not None:
                                    package.writestr(name, text)
                    else:
                        with tarfile.open(archive, "w:gz") as package:
                            for name, text in files.items():
                                if text is not None:
                                    item = tarfile.TarInfo(name)
                                    item.size = len(text)
                                    package.addfile(item, io.BytesIO(text))
                build({})
                runner.check_archive_notices(archive, sdk, prefix)
                for name in texts:
                    for changed in (None, b"", b"Another component's notice"):
                        build({prefix + name: changed})
                        with self.assertRaisesRegex(RuntimeError, "notice missing or changed"):
                            runner.check_archive_notices(archive, sdk, prefix)
                build({})
                (sdk / "NOTICE").write_bytes(b" \n")
                with self.assertRaisesRegex(RuntimeError, "Empty SDK"):
                    runner.check_archive_notices(archive, sdk, prefix)

    def test_sdk_notice_links_cannot_replace_regular_notice_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("LICENSE", "NOTICE"):
                (root / name).write_bytes(b"Project notice")
            archive = root / "sdk.crate"
            with tarfile.open(archive, "w:gz") as package:
                item = tarfile.TarInfo("package/LICENSE")
                item.type = tarfile.SYMTYPE
                item.linkname = "../LICENSE"
                package.addfile(item)
            with self.assertRaisesRegex(RuntimeError, "notice missing or changed"):
                runner.check_archive_notices(archive, root, "package/")

    def test_default_all_is_ordered_and_reports_every_selection(self):
        for action in ("install", "generate", "check", "package"):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "output/artifacts/sdk").mkdir(parents=True)
                with patch.object(runner, "ROOT", root), patch.object(runner, "selected", return_value={"passed": True}) as selected, patch.object(runner, "qualification_context", return_value=None), patch.object(runner.sys, "argv", ["runner.py", action]), redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(runner.main(), 0)
                self.assertEqual([call.args for call in selected.call_args_list], [(action, language) for language in ("go", "python", "typescript", "rust")])
                self.assertIn("completed: go, python, typescript, rust", output.getvalue())
                if action == "package":
                    self.assertEqual(set(json.loads((root / "output/artifacts/sdk/manifest.json").read_text())), {"go", "python", "typescript", "rust"})

    def test_all_stops_on_failure_without_stale_success_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = root / "output/artifacts/sdk/manifest.json"
            manifest.parent.mkdir(parents=True)
            manifest.write_text('{"stale":true}')
            with patch.object(runner, "ROOT", root), patch.object(runner, "selected", side_effect=[{}, RuntimeError("build failed")]) as selected, patch.object(runner.sys, "argv", ["runner.py", "package"]), redirect_stdout(io.StringIO()) as output, redirect_stderr(io.StringIO()):
                self.assertEqual(runner.main(), 1)
            self.assertEqual([call.args[1] for call in selected.call_args_list], ["go", "python"])
            self.assertNotIn("completed", output.getvalue())
            self.assertFalse(manifest.exists())

    def test_go_selection_never_imports_another_sdk_toolchain(self):
        for action, name in (("install", "install"), ("generate", "check"), ("check", "check"), ("package", "package")):
            with patch.object(runner, name) as execute, patch("builtins.__import__", side_effect=AssertionError("unselected toolchain")):
                runner.selected(action, "go")
            execute.assert_called_once_with(*((action,) if name == "check" else ()))

    def test_packaging_failures_do_not_generate_test_install_or_retain_outputs(self):
        for module, failed_build in ((runner, 1), (python_runner, 1), (python_runner, 2), (typescript_runner, 1)):
            with self.subTest(module=module.__name__, failed_build=failed_build), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for name in ("VERSION",):
                    (root / name).write_text("0.1.0\n")
                (root / "go.mod").write_text("module fixture\ngo 1.27.0\n")
                selected = {"node": "/qualified/node", "npm": "/qualified/npm"} if module is typescript_runner else {} if module is python_runner else Path("/qualified/go")
                failure = subprocess.CalledProcessError(1, ["owned-build"])
                attempts = []
                def build(args, *positional, **kwargs):
                    attempts.append(args)
                    if len(attempts) == failed_build:
                        raise failure
                    # A wheel exists when the independent sdist build fails.
                    dist = Path(args[args.index("--outdir") + 1])
                    dist.mkdir()
                    (dist / "partial.whl").write_bytes(b"partial build output")
                with patch.object(module, "ROOT", root), patch.object(module, "installed", return_value=selected), patch.object(module, "inputs" if module is not python_runner else "identity", return_value={}), patch.object(module, "generate", side_effect=AssertionError("no generator")), patch.object(module, "run", side_effect=build) as run, patch.object(runner, "retain") as retain:
                    with self.assertRaises(subprocess.CalledProcessError):
                        module.package()
                    self.assertEqual(run.call_count, failed_build)
                    self.assertNotIn("test", [str(arg) for arg in run.call_args.args[0]])
                    retain.assert_not_called()
                self.assertEqual(list((root / "output/sdk").glob("*")), [])

    def test_python_pep440_version_mapping_and_stale_rejection(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "VERSION").write_text("1.2.3-rc.2\n")
            (root / "pyproject.toml").write_text('[project]\nversion="1.2.3rc2"\n')
            with patch.object(python_runner, "ROOT", root), patch.object(python_runner, "SDK", root):
                self.assertEqual(python_runner.package_version(), "1.2.3rc2")
                (root / "pyproject.toml").write_text('[project]\nversion="1.2.3"\n')
                with self.assertRaisesRegex(RuntimeError, "version differs"):
                    python_runner.package_version()

    def test_python_sdist_refuses_links_and_incomplete_source_before_installation(self):
        for links in (False, True):
            with self.subTest(links=links), tempfile.TemporaryDirectory() as directory:
                work = Path(directory)
                (work / "dist").mkdir()
                with tarfile.open(work / "dist/source.tar.gz", "w:gz") as archive:
                    member = tarfile.TarInfo("package/README.md")
                    if links:
                        member.type = tarfile.SYMTYPE
                        member.linkname = "/outside"
                    archive.addfile(member)
                with patch.object(python_runner, "run") as run:
                    with self.assertRaisesRegex(RuntimeError, "links" if links else "incomplete"):
                        python_runner.qualify_package(work, {}, work / "consumers")
                    run.assert_not_called()

    def test_python_sdist_mismatch_stops_before_any_consumer_install(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            (work / "dist").mkdir()
            with tarfile.open(work / "dist/source.tar.gz", "w:gz") as archive:
                member = tarfile.TarInfo("package/pyproject.toml")
                archive.addfile(member)
            for name, value in (("dist", b"direct"), ("rebuilt", b"different")):
                (work / name).mkdir(exist_ok=True)
                with zipfile.ZipFile(work / name / "package.whl", "w") as archive:
                    archive.writestr("catalog_sdk/__init__.py", value)
            with patch.object(python_runner, "run") as run, patch.object(python_runner, "installed") as installed:
                with self.assertRaisesRegex(RuntimeError, "wheel contents differ"):
                    python_runner.qualify_package(work, {}, work / "consumers")
                self.assertEqual(run.call_count, 1)  # Only the source-archive build.
                installed.assert_not_called()

    def test_manifest_binds_local_artifact_release_contract_and_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "api/openapi").mkdir(parents=True)
            (root / "api/openapi/control-v1.yaml").write_text("contract")
            (root / "VERSION").write_text("0.1.0\n")
            artifact = root / "package.tgz"
            artifact.write_bytes(b"owned package")
            source = root / "source.go"
            source.write_text("package catalogclient\n")
            with patch.object(runner, "ROOT", root), redirect_stdout(io.StringIO()):
                report = runner.retain("go", [artifact], {"inputs": {"lock": "digest"}}, source_files=[source])
            self.assertEqual(report["releaseBase"], "0.1.0")
            self.assertEqual(report["sources"], {"source.go": runner.digest(source)})
            self.assertEqual(report["artifacts"], {"package.tgz": runner.digest(artifact)})
            self.assertEqual(report["openapiSha256"], runner.digest(root / "api/openapi/control-v1.yaml"))
            self.assertEqual((root / "output/artifacts/sdk/go/package.tgz").read_bytes(), artifact.read_bytes())


if __name__ == "__main__":
    unittest.main()
