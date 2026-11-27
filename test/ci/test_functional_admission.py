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
    def test_changed_inputs_select_and_require_their_consumers(self):
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
            shared_jobs = {"charts": True, "ui": True, "docs": True, "languages": ["go", "python", "typescript", "rust"]}
            cases = (
                ("pyproject.toml", shared_jobs, "docs"),
                ("uv.lock", shared_jobs, "charts"),
                ("deployments/helm/open-agentic-catalog/templates/new.yaml", shared_jobs, "charts"),
                ("catalog-ui/scripts/native-test-completion.mjs", {"charts": False, "ui": True, "docs": True, "languages": ["typescript"]}, "clients"),
                ("LICENSE", {"charts": True, "ui": True, "docs": True, "languages": ["go", "typescript"]}, "ui"),
                ("examples/tutorials/local-oidc/service-read.py", {"charts": False, "ui": False, "docs": True, "languages": ["go", "python"]}, "clients"),
            )
            for name, _, _ in cases:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("original fixture\n")
            git("add", ".")
            git("commit", "-qm", "shared input fixtures")
            for name, plan, required_job in cases:
                with self.subTest(name=name):
                    before = git("rev-parse", "HEAD").decode().strip()
                    if name == "LICENSE":
                        git("rm", name)
                    else:
                        (root / name).write_text("changed fixture\n")
                        git("add", name)
                    git("commit", "-qm", "change one shared input")
                    result = subprocess.run([sys.executable, "-B", str(script), "select", "--base", before],
                                            env=env, capture_output=True, text=True, timeout=20, check=True)
                    self.assertEqual(json.loads(result.stdout), plan)
                    results = dict.fromkeys(("selection", "baseline", "unit", "docs", "clients"), "success")
                    results["charts"] = "success" if plan["charts"] else "skipped"
                    results["ui"] = "success" if plan["ui"] else "skipped"
                    for outcome in ("success", "failure", "cancelled", "skipped"):
                        completion = subprocess.run([sys.executable, "-B", str(script), "outcomes", "--plan", result.stdout,
                                                     "--results", json.dumps({**results, required_job: outcome})],
                                                    env=env, capture_output=True, timeout=20, check=False)
                        self.assertEqual(completion.returncode == 0, outcome == "success", completion.stderr)
                        if outcome != "success":
                            self.assertIn(f"{required_job} ({outcome})".encode(), completion.stderr)

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
            self.assertEqual(json.loads(result.stdout), {"charts": False, "ui": True, "docs": True, "languages": ["rust"]})

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
    def test_release_ssh_signer_tag_target_and_main_ancestry(self):
        with tempfile.TemporaryDirectory(prefix="catalog-release-signing-") as temporary:
            base = Path(temporary)
            root = base / "repository"
            root.mkdir()
            def git(*args):
                return subprocess.check_output(["git", "-C", root, *args], timeout=20, text=True).strip()
            git("init", "-q", "-b", "main")
            git("config", "user.name", "Release fixture")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "commit.gpgsign", "false")
            git("config", "gpg.format", "ssh")
            git("config", "tag.gpgsign", "false")
            (root / "VERSION").write_text("0.1.0\n")
            git("add", "VERSION")
            git("commit", "-qm", "Initial version")
            commit = git("rev-parse", "HEAD")
            git("update-ref", "refs/remotes/origin/main", commit)
            for name in ("trusted", "other"):
                subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", base / name], check=True, timeout=20)
            signers = base / "allowed"
            signers.write_text('fixture namespaces="git" ' + (base / "trusted.pub").read_text())
            tag = "v0.1.0"
            def sign(key, name=tag):
                git("-c", "user.signingkey=" + str(base / key), "tag", "-fs", name, "-m", "Fixture release")
            sign("trusted")
            self.assertEqual(a.verified_release_tag(root, commit, signers), git("rev-parse", tag))
            with self.assertRaises(ValueError):
                a.verified_release_tag(root, "0" * 40, signers)
            for kind in ("lightweight", "unsigned", "unknown signer", "renamed", "off main"):
                with self.subTest(kind=kind):
                    if kind == "lightweight":
                        git("tag", "-f", tag)
                    elif kind == "unsigned":
                        git("tag", "-fa", tag, "-m", "Unsigned fixture")
                    elif kind == "unknown signer":
                        sign("other")
                    elif kind == "renamed":
                        sign("trusted", "other-name")
                        git("update-ref", "refs/tags/" + tag, git("rev-parse", "other-name"))
                    else:
                        git("commit", "--allow-empty", "-qm", "Outside protected main")
                        commit = git("rev-parse", "HEAD")
                        sign("trusted")
                    with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                        a.verified_release_tag(root, commit, signers)

    def test_qualification_selects_one_complete_attempt(self):
        script = a.ROOT / "test/ci/admission.py"
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for attempt in ("100-1", "100-2"):
                selected = directory / attempt
                selected.mkdir()
                cmd = [sys.executable, "-B", script, "release-inputs", "--key", attempt]
                pattern = subprocess.check_output(cmd, text=True).strip()
                self.assertTrue(pattern.startswith("pattern={") and pattern.endswith("}"))
                names = pattern[len("pattern={"):-1].split(",")
                self.assertEqual(len(set(names)), 19)
                for name in names:
                    self.assertIn("-" + attempt, name)
                    (selected / name).mkdir()
                check = [*cmd, "--directory", selected]
                subprocess.run(check, check=True)
                (selected / names[0]).rename(selected / "candidate-100-9-backend-amd64")
                self.assertNotEqual(subprocess.run(check, capture_output=True).returncode, 0)

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
            results = dict.fromkeys(("source", "security", "clients", "charts", "candidates", "runtime", "native"), "success")
            assembled = root / "assembled"
            value = delivery.assemble(inputs, results, assembled)
            downloads = root / "downloads"
            downloads.mkdir()
            for name, source in delivery.public_assets(assembled, value).items():
                shutil.copyfile(source, downloads / name)
            shutil.copyfile(assembled / "SHA256SUMS", downloads / "SHA256SUMS")
            result = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=downloads, capture_output=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(result.stdout.splitlines()), 21)
            version = (delivery.ROOT / "VERSION").read_text().strip()
            editor = downloads / f"open-agentic-catalog-{version}.vsix"
            self.assertTrue(editor.is_file())
            editor.write_bytes(b"changed")
            failed = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=downloads, capture_output=True,
                                    env={**os.environ, "LC_ALL": "C"}, timeout=20)
            self.assertNotEqual(failed.returncode, 0)
            self.assertIn(f"{editor.name}: FAILED", failed.stdout.decode())
