"""Real Git/process admission checks in a disposable source fixture; no remote."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import admission as a
import candidate as c


class AdmissionFunctionalTests(unittest.TestCase):
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
