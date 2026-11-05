"""Release policy, archive and delivery tests. External commands are intercepted."""

import copy
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import admission as a
import candidate as c
import release_delivery as r
import release_packages as p
import test_unit_admission as fixtures

SOURCE = fixtures.SOURCE


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        for name in ("run", "check_output"):
            guard = patch.object(r.subprocess, name, side_effect=AssertionError("Unexpected process"))
            guard.start()
            self.addCleanup(guard.stop)

    def test_event_table_requires_exact_ref_and_explicit_activation(self):
        base = {"GITHUB_REPOSITORY": a.REPOSITORY, "GITHUB_REF": "refs/tags/v0.1.0", "GITHUB_EVENT_NAME": "push"}
        self.assertEqual(a.release_intent(base, "0.1.0"), {"upload": True, "publish": False})
        enabled = {**base, "RELEASE_PUBLISH_ENABLED": "true"}
        self.assertTrue(a.release_intent(enabled, "0.1.0")["publish"])
        for key, values in {"RELEASE_PUBLISH_ENABLED": ("false", "TRUE", "1", ""),
                            "GITHUB_REPOSITORY": ("fork/catalog", "")}.items():
            for value in values:
                self.assertFalse(a.release_intent({**enabled, key: value}, "0.1.0")["publish"])
        for event in ("pull_request", "pull_request_target", "release", ""):
            with self.assertRaises(ValueError):
                a.release_intent({**enabled, "GITHUB_EVENT_NAME": event}, "0.1.0")
        for ref in ("refs/heads/main", "refs/tags/v0.2.0", "refs/tags/0.1.0", "refs/tags/v0.1.0/extra"):
            with self.assertRaises(ValueError):
                a.release_intent({**enabled, "GITHUB_REF": ref}, "0.1.0")
        with self.assertRaises(ValueError):
            a.release_intent({**enabled, "ACT": "true"}, "0.1.0")
        manual = {**enabled, "GITHUB_EVENT_NAME": "workflow_dispatch", "INPUT_UPLOAD": "true", "INPUT_PUBLISH": "false"}
        self.assertEqual(a.release_intent(manual, "0.1.0"), {"upload": True, "publish": False})
        self.assertTrue(a.release_intent({**manual, "INPUT_PUBLISH": "true"}, "0.1.0")["publish"])
        for key in ("INPUT_UPLOAD", "INPUT_PUBLISH"):
            for value in ("", "TRUE", "0"):
                with self.assertRaises(ValueError):
                    a.release_intent({**manual, key: value}, "0.1.0")
        rc = {**enabled, "GITHUB_REF": "refs/tags/v0.2.0-rc.1"}
        self.assertTrue(a.release_intent(rc, "0.2.0-rc.1")["publish"])

    def fixture(self, root):
        inputs = root / "inputs"
        inputs.mkdir()
        fixtures.AdmissionTests().release_fixture(inputs)
        results = dict.fromkeys(("source", "security", "clients", "charts", "candidates", "runtime"), "success")
        with patch.object(a, "source_identity", return_value=SOURCE):
            value = r.assemble(inputs, results, root / "release")
        return inputs, root / "release", value

    def test_public_checksums_match_flat_downloads_and_exclude_internal_archives(self):
        with tempfile.TemporaryDirectory() as tmp:
            _, directory, value = self.fixture(Path(tmp))
            assets = r.public_assets(directory, value)
            self.assertEqual(len(assets), 12)
            self.assertTrue({"client-go.tar.gz", "catalogctl-0.1.0-linux-arm64.tar.gz", "open-agentic-catalog-0.1.0.vsix"} <= assets.keys())
            lines = (directory / "SHA256SUMS").read_text().splitlines()
            self.assertEqual({line.split("  ")[1] for line in lines}, set(assets))
            for line in lines:
                checksum, name = line.split("  ")
                self.assertEqual(checksum, c.digest(assets[name]))
                self.assertNotIn("/", name)
            with tarfile.open(directory / "release-evidence.tar.gz") as archive:
                self.assertFalse(any(n.endswith(("image.tar", ".vsix", ".crate", ".whl", ".tgz")) for n in archive.getnames()))
            self.assertIn("images/backend-amd64/image.tar", value["files"])
            changed = copy.deepcopy(value)
            changed["files"]["sdk/python/client.tgz"] = "hash"
            with self.assertRaisesRegex(ValueError, "collision"):
                r.public_assets(directory, changed)

    def test_assembly_rejects_changed_editor_cli_and_source_without_creating_output(self):
        for field in ("editor-sdk", "editor-cleanup", "editor-version", "editor-bytes", "cli-platform", "cli-image", "cli-bytes", "missing-cli"):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as tmp, patch.object(a, "source_identity", return_value=SOURCE):
                root = Path(tmp)
                inputs = root / "inputs"
                inputs.mkdir()
                fixtures.AdmissionTests().release_fixture(inputs)
                receipt = inputs / ("editor/editor.json" if field.startswith("editor") else "cli-amd64/cli.json")
                value = json.loads(receipt.read_text())
                if field == "missing-cli":
                    receipt.unlink()
                elif field.endswith("bytes"):
                    (receipt.parent / value["artifact"]).write_bytes(b"changed")
                else:
                    key = {"editor-sdk": "sdkSha256", "editor-cleanup": "cleanup", "editor-version": "releaseBase", "cli-platform": "platform", "cli-image": "imageId"}[field]
                    value[key] = "changed"
                    receipt.write_text(json.dumps(value))
                results = dict.fromkeys(("source", "security", "clients", "charts", "candidates", "runtime"), "success")
                with self.assertRaises(ValueError):
                    r.assemble(inputs, results, root / "output")
                self.assertFalse((root / "output").exists())

    def test_cli_archive_contains_only_cli_and_notices_and_rejects_symlinks(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            extracted, output = root / "extracted", root / "output"
            (extracted / "licenses/project").mkdir(parents=True)
            output.mkdir()
            (extracted / "catalogctl").write_bytes(b"static binary")
            (extracted / "catalogd").write_bytes(b"not distributed here")
            (extracted / "licenses/project/LICENSE").write_bytes(b"Apache")
            artifact = p.archive_cli(extracted, output, "0.1.0-rc.1", "linux/arm64")
            with tarfile.open(artifact) as archive:
                self.assertEqual(archive.getnames(), ["catalogctl", "licenses/project/LICENSE"])
                self.assertEqual(archive.getmember("catalogctl").mode, 0o555)
            (extracted / "licenses/link").symlink_to(extracted / "catalogd")
            with self.assertRaisesRegex(ValueError, "Redirected"):
                p.archive_cli(extracted, output, "0.1.0", "linux/amd64")

    def test_editor_handoff_binds_full_tests_and_qualified_sdk_without_exporting_raw_report(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(p, "ROOT", Path(tmp)), patch.object(a, "source_identity", return_value=SOURCE):
            root = Path(tmp)
            directory = fixtures.AdmissionTests().sdk(root)
            sdk = a.sdk_manifest(root, "typescript")
            target = root / "output/artifacts/vscode"
            target.mkdir()
            name = "open-agentic-catalog-0.1.0.vsix"
            (target / name).write_bytes(b"VSIX")
            manifest = {"releaseBase": "0.1.0", "artifact": name, "sha256": c.digest(target / name), "sources": {},
                        "inputs": {"output/artifacts/sdk/typescript/client.tgz": a.digest(directory / "client.tgz")}}
            (target / "manifest.json").write_text(json.dumps(manifest))
            report_path = root / "output/vscode/qualification.json"
            report_path.parent.mkdir()
            report = {"scope": "all", "status": "passed", "phases": dict.fromkeys(p.EDITOR_PHASES, "passed"), "package": manifest, "privateToolPaths": "/private"}
            report_path.write_text(json.dumps(report))
            with patch.object(a, "sdk_manifest", return_value=sdk):
                output = root / "output/editor"
                p.stage_editor(output, SOURCE)
                self.assertEqual({f.name for f in output.iterdir()}, {name, "editor.json"})
                self.assertNotIn("privateToolPaths", (output / "editor.json").read_text())
                for change in ("status", "cleanup", "sdk", "source"):
                    changed = copy.deepcopy(report)
                    if change == "status":
                        changed["status"] = "failed"
                    elif change == "cleanup":
                        changed["phases"]["cleanup"] = "failed"
                    elif change == "sdk":
                        (directory / "client.tgz").write_bytes(b"changed SDK")
                    report_path.write_text(json.dumps(changed))
                    with self.assertRaises(ValueError):
                        p.stage_editor(root / "output/fail", {} if change == "source" else SOURCE)
                    self.assertFalse((root / "output/fail").exists())

    def test_delivery_retries_skip_matching_assets_and_published_release_is_read_only(self):
        for published in (False, True):
            with self.subTest(published=published), tempfile.TemporaryDirectory() as tmp:
                _, directory, value = self.fixture(Path(tmp))
                calls = []
                present = dict.fromkeys(value["images"], True)
                digests = {key: "sha256:" + str(i + 1) * 64 for i, key in enumerate(present)}
                indexes = dict.fromkeys(c.PRODUCTS, True)
                def manifest(reference):
                    product = "ui" if "/catalog-ui:" in reference else "backend"
                    return {"manifests": [{"platform": {"os": "linux", "architecture": arch}, "digest": digests[product + "-" + arch]} for arch in ("amd64", "arm64")]}
                with patch.object(r, "delivery_plan", return_value=value), patch.object(r, "destination_commit", return_value=SOURCE["commit"]), \
                        patch.object(r, "existing_assets", return_value={"draft": not published, "missing": []}), \
                        patch.object(r, "registry_preflight", return_value=(present, digests, indexes)), \
                        patch.object(r, "registry_manifest", side_effect=manifest), patch.object(r, "registry_digest", return_value="sha256:" + "f" * 64), \
                        patch.object(r.subprocess, "run", side_effect=lambda args, **kw: calls.append(args)):
                    r.publish(directory)
                self.assertEqual(calls, [])
                record = json.loads((directory / "delivery-result.json").read_text())
                self.assertEqual(record["result"], "success")
                self.assertEqual(record["releaseState"], "published" if published else "draft")
                self.assertEqual(record["delivery"], "verified-existing" if published else "images-pushed-files-in-draft")

    def test_missing_draft_asset_upload_has_no_clobber_and_conflicts_precede_writes(self):
        for state in ("upload", "published-incomplete", "tag-moved", "registry-conflict"):
            with self.subTest(state=state), tempfile.TemporaryDirectory() as tmp:
                _, directory, value = self.fixture(Path(tmp))
                missing = [directory / "SHA256SUMS"]
                calls = []
                present = dict.fromkeys(value["images"], True)
                digests = {key: "sha256:" + str(i + 1) * 64 for i, key in enumerate(present)}
                indexes = dict.fromkeys(c.PRODUCTS, True)
                manifests = {"manifests": [{"platform": {"os": "linux", "architecture": arch}, "digest": digests["backend-" + arch]} for arch in ("amd64", "arm64")]}
                def execute(args, **kw):
                    calls.append(args)
                def registry(reference):
                    result = copy.deepcopy(manifests)
                    if "/catalog-ui:" in reference:
                        for item in result["manifests"]:
                            item["digest"] = digests["ui-" + item["platform"]["architecture"]]
                    return result
                with patch.object(r, "delivery_plan", return_value=value), patch.object(r, "destination_commit", return_value=("0" * 40 if state == "tag-moved" else SOURCE["commit"])), \
                        patch.object(r, "existing_assets", side_effect=[{"draft": state != "published-incomplete", "missing": missing}, {"draft": True, "missing": []}]), \
                        patch.object(r, "registry_preflight", side_effect=ValueError("conflict") if state == "registry-conflict" else None, return_value=(present, digests, indexes)), \
                        patch.object(r, "registry_manifest", side_effect=registry), patch.object(r, "registry_digest", return_value="sha256:" + "f" * 64), \
                        patch.object(r.subprocess, "run", side_effect=execute):
                    if state == "upload":
                        r.publish(directory)
                        self.assertEqual(len(calls), 1)
                        self.assertEqual(calls[0][:3], ["gh", "release", "upload"])
                        self.assertNotIn("--clobber", calls[0])
                    else:
                        with self.assertRaises(ValueError):
                            r.publish(directory)
                        self.assertEqual(calls, [])

    def test_remote_tag_peels_annotations_and_rejects_noncommits(self):
        values = [{"object": {"type": "tag", "sha": "c" * 40}}, {"object": {"type": "commit", "sha": SOURCE["commit"]}}]
        with patch.object(r.subprocess, "check_output", side_effect=[json.dumps(v) for v in values]):
            self.assertEqual(r.destination_commit("0.1.0"), SOURCE["commit"])
        with patch.object(r.subprocess, "check_output", return_value=json.dumps({"object": {"type": "tree", "sha": "d" * 40}})), self.assertRaises(ValueError):
            r.destination_commit("0.1.0")

    def test_rc_packages_keep_their_base_while_chart_versions_remain_independent(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "VERSION").write_text("0.2.0-rc.1\n")
            (root / "api/openapi").mkdir(parents=True)
            (root / "api/openapi/control-v1.yaml").write_text("fixture contract")
            with patch.object(r, "ROOT", root):
                _, directory, value = self.fixture(root)
            self.assertEqual(value["build"]["version"], "0.2.0-rc.1")
            self.assertIn("catalogctl-0.2.0-rc.1-linux-arm64.tar.gz", r.public_assets(directory, value))
            self.assertIn("open-agentic-catalog-0.2.0-rc.1.vsix", r.public_assets(directory, value))
            self.assertIn("charts/open-agentic-catalog-0.1.0.tgz", value["files"])

    def test_existing_assets_downloads_only_matching_names_and_refuses_changed_bytes(self):
        with tempfile.TemporaryDirectory() as tmp:
            package = Path(tmp) / "package.tgz"
            missing = Path(tmp) / "SHA256SUMS"
            package.write_bytes(b"tested")
            record = {"tagName": "v0.1.0", "isDraft": True, "assets": [{"name": package.name}]}
            calls = []
            def download(args, **kw):
                calls.append(args)
                directory = Path(args[args.index("--dir") + 1])
                (directory / package.name).write_bytes(package.read_bytes())
            with patch.object(r.subprocess, "check_output", return_value=json.dumps(record)), patch.object(r.subprocess, "run", side_effect=download):
                self.assertEqual(r.existing_assets("0.1.0", [package, missing]), {"draft": True, "missing": [missing]})
            self.assertEqual(len(calls), 1)
            self.assertEqual(calls[0][:3], ["gh", "release", "download"])
            def changed(args, **kw):
                (Path(args[args.index("--dir") + 1]) / package.name).write_bytes(b"different")
            with patch.object(r.subprocess, "check_output", return_value=json.dumps(record)), patch.object(r.subprocess, "run", side_effect=changed), self.assertRaisesRegex(ValueError, "different asset"):
                r.existing_assets("0.1.0", [package])

    def test_editor_command_discards_stale_report_before_running_tests_and_refuses_failure(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(p, "ROOT", Path(tmp)), patch.object(a, "source_identity", return_value=SOURCE):
            root = Path(tmp)
            report = root / "output/vscode/qualification.json"
            report.parent.mkdir(parents=True)
            report.write_text('"stale passing report"')
            def fail(*args, **kw):
                self.assertFalse(report.exists())
                raise RuntimeError("test failed")
            with patch.object(p.subprocess, "run", side_effect=fail), patch.object(p, "stage_editor") as stage, self.assertRaisesRegex(RuntimeError, "test failed"):
                p.check_editor(root / "output/editor")
            stage.assert_not_called()

    def test_first_delivery_and_retry_transfer_exact_members_without_rebuilding(self):
        import hashlib
        for interrupted in (False, True):
            with self.subTest(interrupted=interrupted), tempfile.TemporaryDirectory() as tmp:
                _, directory, value = self.fixture(Path(tmp))
                manifests, digests, tags, calls = {}, {}, {}, []
                uploads = [True]
                fail = [interrupted]
                reads = []
                def inspect(reference):
                    reads.append(reference)
                    return manifests.get(reference)
                def execute(args, **kw):
                    calls.append(args)
                    if args[:3] == ["docker", "image", "load"]:
                        self.assertGreaterEqual(len(reads), 6, "Inspect every destination before writes")
                    elif args[:3] == ["docker", "image", "tag"]:
                        tags[args[4]] = args[3]
                    elif args[:3] == ["docker", "image", "push"]:
                        target = args[3]
                        if fail[0] and "/catalog-ui:" in target:
                            fail[0] = False
                            raise OSError("fixture interrupted delivery")
                        manifests[target] = {"config": {"digest": tags[target]}}
                        digests[target] = "sha256:" + hashlib.sha256(target.encode()).hexdigest()
                    elif args[:4] == ["docker", "buildx", "imagetools", "create"]:
                        target = args[args.index("--tag") + 1]
                        manifests[target] = {"manifests": [{"platform": {"os": "linux", "architecture": arch}, "digest": digests[target + "-" + arch]} for arch in ("amd64", "arm64")]}
                        self.assertEqual(args[-2:], [target.split(":")[0] + "@" + digests[target + "-" + arch] for arch in ("amd64", "arm64")])
                        digests[target] = "sha256:" + hashlib.sha256(target.encode()).hexdigest()
                    elif args[:3] == ["gh", "release", "upload"]:
                        self.assertNotIn("--clobber", args)
                        uploads[0] = False
                    else:
                        self.fail("Unexpected delivery operation: " + str(args[:3]))
                def assets(*args):
                    return {"draft": True, "missing": [*r.public_assets(directory, value).values(), directory / "SHA256SUMS"] if uploads[0] else []}
                with patch.object(r, "delivery_plan", return_value=value), patch.object(r, "destination_commit", return_value=SOURCE["commit"]), \
                        patch.object(r, "existing_assets", side_effect=assets), patch.object(r, "registry_manifest", side_effect=inspect), \
                        patch.object(r, "registry_digest", side_effect=lambda target: digests[target]), patch.object(r.subprocess, "run", side_effect=execute):
                    if interrupted:
                        with self.assertRaisesRegex(OSError, "interrupted"):
                            r.publish(directory)
                        record = json.loads((directory / "delivery-result.json").read_text())
                        self.assertEqual(record["result"], "incomplete")
                        self.assertIn("index-backend", record["completed"])
                        calls.clear()
                    r.publish(directory)
                    if interrupted:
                        self.assertFalse(any("push" in args and "/open-agentic-catalog:" in args[-1] for args in calls))
                    self.assertEqual(json.loads((directory / "delivery-result.json").read_text())["result"], "success")
                    calls.clear()
                    r.publish(directory)
                    self.assertEqual(calls, [], "A matching retry must make no write calls")
