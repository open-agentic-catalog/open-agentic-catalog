"""Real Git/process admission checks in a disposable source fixture; no remote."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import admission as a
import candidate as c


class AdmissionFunctionalTests(unittest.TestCase):
    def test_each_root_python_manifest_selects_and_requires_all_conditional_jobs(self):
        with tempfile.TemporaryDirectory(prefix="catalog-admission-manifests-") as temporary:
            root = Path(temporary)
            env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}

            def git(*arguments):
                return subprocess.check_output(["git", "-C", str(root), *arguments], env=env, stderr=subprocess.DEVNULL, timeout=20)

            git("init", "-q")
            git("config", "user.name", "Local fixture")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "commit.gpgsign", "false")
            (root / "test/ci").mkdir(parents=True)
            script = root / "test/ci/admission.py"
            script.write_bytes(Path(a.__file__).read_bytes())
            (root / "documentation").mkdir()
            (root / "documentation/content-map.json").write_text("[]")
            for name in ("pyproject.toml", "uv.lock"):
                (root / name).write_text("initial shared Python tool input\n")
            git("add", ".")
            git("commit", "-qm", "fixture before shared manifests")
            for name in ("pyproject.toml", "uv.lock"):
                with self.subTest(name=name):
                    before = git("rev-parse", "HEAD").decode().strip()
                    (root / name).write_text("changed shared Python tool input with a new tool pin\n")
                    git("add", name)
                    git("commit", "-qm", "fixture shared manifest")
                    result = subprocess.run([sys.executable, "-B", str(script), "select", "--base", before],
                                            env=env, capture_output=True, text=True, timeout=20, check=True)
                    self.assertEqual(json.loads(result.stdout), {"ui": True, "docs": True, "languages": ["go", "python", "typescript", "rust"]})
                    results = dict.fromkeys(("baseline", "unit", "ui", "docs", "clients"), "success")
                    for outcome in ("success", "failure", "cancelled", "skipped"):
                        completion = subprocess.run([sys.executable, "-B", str(script), "outcomes", "--plan", result.stdout,
                                                     "--results", json.dumps({**results, "docs": outcome})],
                                                    env=env, capture_output=True, timeout=20, check=False)
                        self.assertEqual(completion.returncode == 0, outcome == "success", completion.stderr)

    def test_renamed_inputs_select_both_original_and_destination_owners(self):
        with tempfile.TemporaryDirectory(prefix="catalog-admission-rename-") as temporary:
            root = Path(temporary)
            env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}

            def git(*arguments):
                return subprocess.check_output(["git", "-C", str(root), *arguments], env=env, stderr=subprocess.DEVNULL, timeout=20)

            git("init", "-q")
            git("config", "user.name", "Local fixture")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "commit.gpgsign", "false")
            git("config", "diff.renames", "true")
            (root / "test/ci").mkdir(parents=True)
            (root / "test/ci/admission.py").write_bytes(Path(a.__file__).read_bytes())
            (root / "documentation").mkdir()
            (root / "documentation/content-map.json").write_text("[]")
            (root / "sdk/rust").mkdir(parents=True)
            (root / "sdk/rust/README.md").write_text("Published SDK guide\n")
            (root / "misc").mkdir()
            (root / "misc/client.txt").write_text("UI input\n")
            git("add", ".")
            git("commit", "-qm", "fixture before renames")
            before = git("rev-parse", "HEAD").decode().strip()
            (root / "catalog-ui/src").mkdir(parents=True)
            git("mv", "sdk/rust/README.md", "misc/archive.md")
            git("mv", "misc/client.txt", "catalog-ui/src/client.txt")
            git("commit", "-qm", "fixture renames")
            result = subprocess.run(["python3", "-B", str(root / "test/ci/admission.py"), "select", "--base", before],
                                    env=env, capture_output=True, text=True, timeout=20, check=True)
            self.assertEqual(json.loads(result.stdout), {"ui": True, "docs": True, "languages": ["rust"]})

    def test_git_source_receipt_and_stale_package_refusal(self):
        with tempfile.TemporaryDirectory(prefix="catalog-admission-") as temporary:
            root = Path(temporary)
            env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}

            def git(*arguments):
                return subprocess.check_output(["git", "-C", str(root), *arguments], env=env, stderr=subprocess.DEVNULL, timeout=20)

            git("init", "-q")
            git("config", "user.name", "Local fixture")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "commit.gpgsign", "false")
            (root / "VERSION").write_text("0.1.0\n")
            (root / ".gitignore").write_text("/output/\n")
            (root / "api/openapi").mkdir(parents=True)
            (root / "api/openapi/control-v1.yaml").write_text("api")
            (root / "client.ts").write_text("source")
            git("add", ".")
            git("commit", "-qm", "fixture")
            source = a.source_identity(root)
            self.assertEqual(len(source["commit"]), 40)
            self.assertEqual(a.source_identity(root), source)
            directory = root / "output/artifacts/sdk/typescript"
            directory.mkdir(parents=True)
            (directory / "client.tgz").write_bytes(b"qualified fixture")
            value = {"language": "typescript", "releaseBase": "0.1.0", "openapiSha256": a.digest(root / "api/openapi/control-v1.yaml"),
                     "tools": {"node": "pinned"}, "sources": {"client.ts": a.digest(root / "client.ts")},
                     "artifacts": {"client.tgz": a.digest(directory / "client.tgz")}}
            (directory / "manifest.json").write_text(json.dumps(value))
            a.qualify_sdk(root, "typescript", "local-1", source)
            a.stage_sdk(root, "typescript", "local-1", root / "output/artifacts/qualified-sdk/local-1")
            self.assertEqual(a.source_identity(root), source, "Ignored reports cannot alter source identity")
            (root / "client.ts").write_text("changed source")
            with self.assertRaises(ValueError):
                a.stage_sdk(root, "typescript", "local-1", root / "output/artifacts/qualified-sdk/local-2")
            self.assertNotEqual(a.source_identity(root), source)
            # The CLI must refuse invalid selections with a failing process.
            result = subprocess.run(["python3", "-B", str(a.ROOT / "test/ci/admission.py"), "languages", "invalid"],
                                    capture_output=True, timeout=20, check=False)
            self.assertNotEqual(result.returncode, 0)

    def test_candidate_archive_identity_and_platform_substitution(self):
        with tempfile.TemporaryDirectory(prefix="catalog-candidate-") as temporary:
            directory = Path(temporary)
            source = {"commit": "a" * 40, "sourceSha256": "b" * 64}
            (directory / "image.tar").write_bytes(b"fixture archive")
            (directory / "inventory.spdx.json").write_text("{}")
            image_id = "sha256:" + "c" * 64
            (directory / "scan-summary.json").write_text(json.dumps({"outcome": "passed", "imageId": image_id, "platform": "linux/amd64"}))
            value = {"schemaVersion": 1, "product": "backend", "platform": "linux/amd64", "imageId": image_id,
                     "build": {"version": "0.1.0", "commit": source["commit"], "sourceState": "clean"}, "source": source,
                     "checks": {"inspection": "success", "sbom": "success", "scan": "success"}, "files": c.inventory(directory)}
            descriptor = directory / "candidate.json"
            descriptor.write_text(json.dumps(value))
            self.assertEqual(c.read_candidate(descriptor, identity=source)["imageId"], image_id)
            with self.assertRaises(ValueError):
                c.read_candidate(descriptor, platform="linux/arm64")
            with self.assertRaises(ValueError):
                c.read_candidate(descriptor, identity={**source, "commit": "d" * 40})
            (directory / "image.tar").write_bytes(b"substituted archive")
            with self.assertRaises(ValueError):
                c.read_candidate(descriptor, identity=source)
            # Loading a corrupt archive must fail before any Docker invocation.
            with patch.object(a, "source_identity", return_value=source), patch.object(c, "run", side_effect=AssertionError("must not start Docker")):
                with self.assertRaises(ValueError):
                    c.admit(descriptor, "backend", load=True)


class ReleaseTagFunctionalTests(unittest.TestCase):
    def test_annotated_fixture_tag_resolves_commit_and_dirty_source_still_refuses(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location("release_fixture_versioning", a.ROOT / "test/packaging/versioning.py")
        versioning = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(versioning)
        with tempfile.TemporaryDirectory(prefix="catalog-release-tag-") as temporary:
            root = Path(temporary)
            environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
            def git(*args):
                return subprocess.check_output(["git", "-C", str(root), *args], env=environment, stderr=subprocess.DEVNULL, timeout=20).decode().strip()
            git("init", "-q")
            git("config", "user.name", "Local fixture")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "commit.gpgsign", "false")
            (root / "VERSION").write_text("0.1.0\n")
            git("add", "VERSION")
            git("commit", "-qm", "Disposable release policy fixture")
            git("-c", "tag.gpgsign=false", "tag", "-a", "v0.1.0", "-m", "Disposable fixture tag")
            self.assertNotEqual(git("rev-parse", "v0.1.0"), git("rev-parse", "HEAD"))
            self.assertEqual(versioning.release(root)["commit"], git("rev-parse", "v0.1.0^{commit}"))
            self.assertEqual(versioning.release(root)["version"], "0.1.0")
            with self.assertRaises(ValueError):
                a.release_intent({"GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main"}, "0.1.0")
            (root / "VERSION").write_text("0.1.1\n")
            with self.assertRaises(ValueError):
                versioning.release(root)

    def test_flat_download_directory_passes_standard_checksum_verification(self):
        import shutil
        import release_delivery as delivery
        import test_unit_admission as fixtures
        with tempfile.TemporaryDirectory(prefix="catalog-downloads-") as temporary, patch.object(a, "source_identity", return_value=fixtures.SOURCE):
            root = Path(temporary)
            inputs = root / "inputs"
            inputs.mkdir()
            fixtures.AdmissionTests().release_fixture(inputs)
            results = dict.fromkeys(("source", "security", "clients", "charts", "candidates", "runtime"), "success")
            assembled = root / "assembled"
            value = delivery.assemble(inputs, results, assembled)
            downloads = root / "downloads"
            downloads.mkdir()
            for name, source in delivery.public_assets(assembled, value).items():
                shutil.copyfile(source, downloads / name)
            shutil.copyfile(assembled / "SHA256SUMS", downloads / "SHA256SUMS")
            result = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=downloads, capture_output=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(result.stdout.splitlines()), 12)
            (downloads / "open-agentic-catalog-0.1.0.vsix").write_bytes(b"changed")
            failed = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=downloads, capture_output=True, timeout=20)
            self.assertNotEqual(failed.returncode, 0)
